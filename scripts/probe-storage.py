#!/usr/bin/env python3
"""Actual native storage/expansion journeys against disposable API fixtures."""
import argparse
from copy import deepcopy
from datetime import datetime, timezone
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import struct
import subprocess
import tempfile
import termios
import threading
from urllib.parse import parse_qs, urlparse


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--repo', type=Path, required=True)
parser.add_argument('--binary', type=Path, required=True)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
spec = importlib.util.spec_from_file_location('journeys', args.repo / 'scripts/regression-journeys.py')
journeys = importlib.util.module_from_spec(spec)
spec.loader.exec_module(journeys)
demo = journeys.demo
demo.GROUPS['v1'].extend([('persistentvolumeclaims', 'PersistentVolumeClaim', True),
                          ('persistentvolumes', 'PersistentVolume', False)])
demo.GROUPS['storage.k8s.io/v1'] = [('storageclasses', 'StorageClass', False),
    ('csidrivers', 'CSIDriver', False), ('csinodes', 'CSINode', False),
    ('volumeattachments', 'VolumeAttachment', False)]


class Handler(demo.APIHandler):
    def reply(self, obj):
        try:
            super().reply(obj)
        except (BrokenPipeError, ConnectionResetError):
            # Native watches/reads are canceled when the terminal closes.
            pass

    def record(self, method):
        parsed = urlparse(self.path)
        with self.server.lock:
            self.server.requests.append({'method': method, 'path': parsed.path,
                                         'query': parse_qs(parsed.query)})
        return parsed

    def failure(self, code, reason, message):
        body = json.dumps({'apiVersion': 'v1', 'kind': 'Status', 'status': 'Failure',
                           'code': code, 'reason': reason, 'message': message}).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.record('GET').path
        if path.endswith('/csidrivers'):
            return self.failure(403, 'Forbidden', 'Fixture CSI driver access denied')
        if path == '/api/v1/namespaces/apps/persistentvolumeclaims/data' and self.server.deny_selected:
            return self.failure(503, 'ServiceUnavailable', 'Fixture selected PVC unavailable')
        if path.startswith('/apis/metrics.k8s.io/'):
            return self.failure(503, 'ServiceUnavailable', 'Fixture metrics unavailable')
        return super().do_GET()

    def do_POST(self):
        self.record('POST')
        return super().do_POST()

    def do_PATCH(self):
        path = self.record('PATCH').path
        if path != '/api/v1/namespaces/apps/persistentvolumeclaims/data':
            return self.failure(405, 'MethodNotAllowed', 'Unexpected fixture mutation')
        patch = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        with self.server.lock:
            obj = next(o for o in self.server.objects if o['kind'] == 'PersistentVolumeClaim')
            expected = [{'op': 'test', 'path': '/metadata/uid', 'value': obj['metadata']['uid']},
                        {'op': 'test', 'path': '/metadata/resourceVersion', 'value': obj['metadata']['resourceVersion']},
                        {'op': 'replace', 'path': '/spec/resources/requests/storage', 'value': '15Gi'}]
            if patch != expected or self.headers.get('Content-Type') != 'application/json-patch+json':
                return self.failure(409, 'Conflict', 'Fixture conditional size-only patch rejected')
            self.server.patches.append(patch)
            obj['spec']['resources']['requests']['storage'] = '15Gi'
            obj['metadata']['resourceVersion'] = '101'
            response = deepcopy(obj)
        # Status capacity and FilesystemResizePending deliberately stay retained.
        return self.reply(response)

    def do_PUT(self):
        self.record('PUT')
        return self.failure(405, 'MethodNotAllowed', 'Unexpected fixture mutation')

    def do_DELETE(self):
        self.record('DELETE')
        return self.failure(405, 'MethodNotAllowed', 'Unexpected fixture mutation')


api = journeys.JourneyAPI()
api.RequestHandlerClass = Handler
api.lock = threading.Lock()
api.requests, api.patches = [], []
api.deny_selected = False
api.objects = [o for o in api.objects if o['kind'] not in ('Secret', 'Pod', 'Event')]
stamp = datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')
api.objects.extend([
    {'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'data', 'namespace': 'apps',
        'uid': 'fixture-pvc', 'resourceVersion': '100', 'creationTimestamp': stamp},
     'spec': {'volumeName': 'data-pv', 'storageClassName': 'fast', 'accessModes': ['ReadWriteOnce'],
              'resources': {'requests': {'storage': '12Gi'}}},
     'status': {'phase': 'Bound', 'capacity': {'storage': '8Gi'}, 'accessModes': ['ReadWriteOnce'],
                'conditions': [{'type': 'FileSystemResizePending', 'status': 'True',
                                'reason': 'AwaitingNodeExpansion', 'lastTransitionTime': stamp}],
                'allocatedResourceStatuses': {'storage': 'NodeResizePending'}}},
    {'apiVersion': 'v1', 'kind': 'PersistentVolume', 'metadata': {'name': 'data-pv', 'uid': 'fixture-pv',
        'resourceVersion': '100'}, 'spec': {'capacity': {'storage': '12Gi'}, 'storageClassName': 'fast',
        'accessModes': ['ReadWriteOnce'], 'persistentVolumeReclaimPolicy': 'Retain',
        'claimRef': {'namespace': 'apps', 'name': 'data', 'uid': 'fixture-pvc'},
        'csi': {'driver': 'fixture.csi', 'volumeHandle': 'not-retained-sensitive-handle',
                'volumeAttributes': {'password': 'not-retained-sensitive-value'}},
        'nodeAffinity': {'required': {'nodeSelectorTerms': [{'matchExpressions': [{'key': 'topology.kubernetes.io/zone',
            'operator': 'In', 'values': ['zone-a']}]}]}}}, 'status': {'phase': 'Bound'}},
    {'apiVersion': 'storage.k8s.io/v1', 'kind': 'StorageClass', 'metadata': {'name': 'fast', 'uid': 'fixture-sc',
        'resourceVersion': '100'}, 'provisioner': 'fixture.csi', 'allowVolumeExpansion': True,
     'volumeBindingMode': 'WaitForFirstConsumer', 'parameters': {'password': 'not-retained-sensitive-value'},
     'allowedTopologies': [{'matchLabelExpressions': [{'key': 'topology.kubernetes.io/zone', 'values': ['zone-a']}]}]},
    {'apiVersion': 'storage.k8s.io/v1', 'kind': 'CSINode', 'metadata': {'name': 'fixture-node', 'uid': 'fixture-csi-node'},
     'spec': {'drivers': [{'name': 'fixture.csi', 'nodeID': 'not-retained-node-id',
        'topologyKeys': ['topology.kubernetes.io/zone'], 'allocatable': {'count': 16}}]}},
    {'apiVersion': 'storage.k8s.io/v1', 'kind': 'VolumeAttachment', 'metadata': {'name': 'data-attachment',
        'uid': 'fixture-attachment'}, 'spec': {'attacher': 'fixture.csi', 'nodeName': 'fixture-node',
        'source': {'persistentVolumeName': 'data-pv'}}, 'status': {'attached': False,
        'attachError': {'time': stamp, 'message': 'Fixture attachment permission failure'}}},
    {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'storage-consumer', 'namespace': 'apps',
        'uid': 'fixture-storage-pod', 'resourceVersion': '100', 'creationTimestamp': stamp},
     'spec': {'nodeName': 'fixture-node', 'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': 'data'}}],
              'containers': [{'name': 'api', 'image': 'fixture/api:v1'}]}, 'status': {'phase': 'Pending'}},
    {'apiVersion': 'v1', 'kind': 'Event', 'metadata': {'name': 'mount-fault', 'namespace': 'apps',
        'uid': 'fixture-event', 'resourceVersion': '100'}, 'involvedObject': {'kind': 'Pod', 'namespace': 'apps',
        'name': 'storage-consumer', 'uid': 'fixture-storage-pod'}, 'reason': 'FailedMount', 'type': 'Warning',
     'message': 'Fixture mount timeout; another Pod volume may be involved', 'lastTimestamp': stamp, 'count': 3},
])
threading.Thread(target=api.serve_forever, daemon=True).start()
args.output.mkdir(parents=True, exist_ok=True)
captures = []
demo.COLS, demo.ROWS = 120, 34


def resize(terminal, cols, rows):
    fcntl.ioctl(terminal.master, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
    terminal.screen.resize(rows, cols)
    demo.COLS, demo.ROWS = cols, rows
    os.kill(terminal.process.pid, signal.SIGWINCH)
    terminal.drain(.65)


def finish(terminal):
    os.write(terminal.master, b':quit\r')
    terminal.process.wait(timeout=5)
    if terminal.process.returncode != 0:
        raise AssertionError('Normal keyboard quit failed')
    captures.extend(terminal.captures)


try:
    with tempfile.TemporaryDirectory(prefix='k9plus-storage-readonly-') as directory:
        terminal = demo.Terminal(args.binary.resolve(), directory, api.server_port, command='persistentvolumeclaims apps',
                                 flags=['--readonly'], ui_config='    noIcons: true\n')
        try:
            terminal.drain(4)
            journeys.assert_screen(terminal, ['data', 'Bound'])
            terminal.command('storage')
            terminal.capture(args.output, 'overview-120', ['Storage diagnosis / apps/data', 'partial evidence',
                'Bind: Bound', 'Attach: attached=false', 'Mount: event FailedMount', 'FileSystemResizePending', 'e expand', 'Esc back'])
            for cols, rows in [(80, 24), (60, 24), (40, 16)]:
                resize(terminal, cols, rows)
                terminal.capture(args.output, f'overview-{cols}x{rows}', ['Storage diagnosis', 'partial evidence',
                    'STORAGE STAGES', 'Bind: Bound', 'e expand', 'Esc back'])
            terminal.keys('4', .4)
            terminal.capture(args.output, 'csi-floor-40x16', ['CSI', 'drivers denied', 'e expand', 'Esc back'])
            resize(terminal, 40, 12)
            terminal.capture(args.output, 'floor-notice-40x12', ['View too small'])
            resize(terminal, 60, 24)
            terminal.capture(args.output, 'csi-recovered-60', ['CSI', 'drivers denied', 'metadata does not report'])
            resize(terminal, 80, 24)
            terminal.keys('2', .4)
            terminal.capture(args.output, 'claims-80', ['Requested storage: 12Gi', 'Status capacity: 8Gi | usage N/A',
                'FileSystemResizePending', 'NodeResizePending', 'storage-consumer'])
            terminal.keys('3', .4)
            terminal.capture(args.output, 'topology-80', ['BINDING AND TOPOLOGY', 'fixture-pv', 'claim reference'.capitalize(),
                'topology.kubernetes.io/zone', 'WaitForFirstConsumer', 'allow expansion: true'])
            terminal.keys('5', .4)
            terminal.capture(args.output, 'evidence-80', ['READ-ONLY STORAGE SNAPSHOT', 'fixture-pvc',
                'Independent reads are not atomic', 'PVCs: complete'])
            terminal.keys('\x1b[6~', .4)
            terminal.capture(args.output, 'evidence-sources-80', ['CSIDrivers: denied', 'VolumeAttachments: complete'])
            terminal.keys('e', .4)
            terminal.capture(args.output, 'expansion-input-80', ['PVC expansion preview', 'New requested storage', 'Cancel', 'Preview'])
            terminal.keys('\t15Gi\t\t\r', .4)
            terminal.capture(args.output, 'expansion-confirm-80', ['Confirm PVC expansion', 'Requested: 12Gi -> 15Gi',
                'Observed capacity: 8Gi', 'Submit request', 'Cancel'])
            resize(terminal, 40, 16)
            terminal.capture(args.output, 'expansion-confirm-floor-40x16', ['Confirm PVC expansion', 'Submit request', 'Cancel'])
            resize(terminal, 80, 24)
            terminal.keys('\t\r', .4)
            terminal.capture(args.output, 'expansion-readonly-blocked-80', ['Read-only mode blocks submission', 'Confirm PVC expansion'])
            terminal.keys('\x1b', .4)
            terminal.keys('\x1b', .4)
            terminal.capture(args.output, 'back-pvc-selection-80', ['persistentvolumeclaims', 'data', 'Bound'])
            with api.lock:
                if api.patches:
                    raise AssertionError('Read-only preview wrote a PVC')
            finish(terminal)
        finally:
            terminal.close()

    demo.COLS, demo.ROWS = 80, 24
    with tempfile.TemporaryDirectory(prefix='k9plus-storage-write-') as directory:
        terminal = demo.Terminal(args.binary.resolve(), directory, api.server_port, command='persistentvolumeclaims apps',
                                 ui_config='    noIcons: true\n')
        try:
            terminal.drain(4)
            terminal.command('storage')
            terminal.keys('e', .4)
            terminal.keys('\t15Gi\t\t\r', .4)
            terminal.capture(args.output, 'write-confirm-80', ['Confirm PVC expansion', '12Gi -> 15Gi', 'Submit request'])
            with api.lock:
                if api.patches:
                    raise AssertionError('Preview attempted a write before explicit confirmation')
            terminal.keys('\t\r', 1)
            terminal.command('operations')
            terminal.capture(args.output, 'accepted-receipt-80', ['PVC expansion request', 'ACCEPTED', 'fixture-pvc',
                'controller/filesystem', 'progress unconfirmed'])
            terminal.keys('\x1b', .4)
            terminal.keys('r', .8)
            terminal.keys('2', .4)
            terminal.capture(args.output, 'accepted-still-pending-80', ['Requested storage: 15Gi',
                'Status capacity: 8Gi | usage N/A', 'FileSystemResizePending', 'NodeResizePending'])
            captured = journeys.text(terminal).split('Captured ')[1].split('\n')[0]
            api.deny_selected = True
            terminal.keys('r', .8)
            terminal.capture(args.output, 'failed-refresh-retains-80', ['Retained / failed refresh',
                'Previous captured evidence retained', 'Requested storage: 15Gi', 'Status capacity: 8Gi'])
            if captured not in journeys.text(terminal):
                raise AssertionError('Failed refresh replaced original source/capture time')
            terminal.keys('\x1b', .4)
            terminal.capture(args.output, 'write-back-pvc-selection-80', ['data', 'persistentvolumeclaims'])
            finish(terminal)
        finally:
            terminal.close()

    with api.lock:
        requests, patches = deepcopy(api.requests), deepcopy(api.patches)
    if len(patches) != 1:
        raise AssertionError(f'Expected one explicitly confirmed write, got {len(patches)}')
    writes = [r for r in requests if r['method'] not in ('GET', 'POST')]
    if len(writes) != 1 or writes[0]['method'] != 'PATCH':
        raise AssertionError(f'Unexpected resource mutations: {writes}')
    if any('/secrets' in r['path'] for r in requests):
        raise AssertionError('Secret API requested')
    reads = [r for r in requests if r['query'].get('limit') == ['101']]
    if len(reads) != 24:
        raise AssertionError(f'Expected three fixed bounded eight-source collections, got {len(reads)}')
    (args.output / 'api-requests.json').write_text(json.dumps(requests, indent=2) + '\n')
    (args.output / 'conditional-patch.json').write_text(json.dumps(patches[0], indent=2) + '\n')
    (args.output / 'manifest.json').write_text(json.dumps({'result': 'passed',
        'source_commit': subprocess.check_output(['git', '-C', str(args.repo), 'rev-parse', 'HEAD'], text=True).strip(),
        'source_dirty': bool(subprocess.check_output(['git', '-C', str(args.repo), 'status', '--porcelain'], text=True).strip()),
        'binary_sha256': hashlib.sha256(args.binary.read_bytes()).hexdigest(),
        'coverage': 'Actual emitted native PTY cells; disposable local API fixtures. No live-cluster, CSI-driver integration or operator-study claim.',
        'assertions': ['UID-pinned PVC and independent bind/attach/mount/resize evidence',
            'capacity versus unavailable usage and pending filesystem progress', 'denied CSI and readable topology/attachment retained',
            '120 -> 80 -> 60 -> 40x16 -> 40x12 notice -> 60 retains CSI selection',
            'responsive expansion input/preview, read-only submission blocked', 'one explicitly confirmed conditional UID/RV size-only patch',
            'OPS ACCEPTED receipt is distinct from fresh pending controller/filesystem state',
            'failed refresh keeps previous source/time; Back keeps selected PVC',
            'three bounded eight-source collections; no Secret reads or other resource writes', 'normal keyboard quit'],
        'captures': captures}, indent=2) + '\n')
finally:
    api.stopped.set()
    api.shutdown()
    api.server_close()
