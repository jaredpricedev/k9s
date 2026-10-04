#!/usr/bin/env python3
"""Native capacity workflow against disposable API fixtures; no live integration claim."""
import argparse, fcntl, hashlib, importlib.util, json, os, signal, struct, tempfile, termios, threading, time
from pathlib import Path
from datetime import datetime, timezone
from copy import deepcopy
from urllib.parse import urlparse, parse_qs

parser=argparse.ArgumentParser()
parser.add_argument('--repo',type=Path,required=True)
parser.add_argument('--binary',type=Path,required=True)
parser.add_argument('--output',type=Path,required=True)
a=parser.parse_args()
spec=importlib.util.spec_from_file_location('journeys',a.repo/'scripts/regression-journeys.py')
j=importlib.util.module_from_spec(spec);spec.loader.exec_module(j);demo=j.demo
for source,kind in [('resourcequotas','ResourceQuota'),('limitranges','LimitRange')]:demo.GROUPS['v1'].append((source,kind,True))
demo.GROUPS['autoscaling/v2']=[('horizontalpodautoscalers','HorizontalPodAutoscaler',True)]

class Handler(demo.APIHandler):
 def record(self,method):
  parsed=urlparse(self.path)
  with self.server.lock:self.server.requests.append({'method':method,'path':parsed.path,'query':parse_qs(parsed.query)})
  return parsed
 def failure(self,code,reason,message):
  body=json.dumps({'apiVersion':'v1','kind':'Status','status':'Failure','code':code,'reason':reason,'message':message}).encode()
  self.send_response(code);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
 def do_GET(self):
  path=self.record('GET').path
  if path=='/apis/apps/v1/namespaces/apps/deployments/payments-api' and self.server.deny_selected:return self.failure(503,'ServiceUnavailable','Fixture selected resource unavailable')
  if path=='/api/v1/nodes' and not self.server.healthy:return self.failure(403,'Forbidden','Fixture node access denied')
  if path.startswith('/apis/autoscaling.k8s.io/v1/'):return self.failure(404,'NotFound','Optional VPA API not installed in fixture')
  if path=='/apis/metrics.k8s.io/v1beta1/namespaces/apps/pods':
   if not self.server.healthy:return self.failure(503,'ServiceUnavailable','Fixture metrics unavailable')
   now=datetime.now(timezone.utc).isoformat().replace('+00:00','Z')
   items=[]
   for obj in self.server.objects:
    if obj['kind']=='Pod' and obj['metadata'].get('labels',{}).get('app')=='payments':
     items.append({'apiVersion':'metrics.k8s.io/v1beta1','kind':'PodMetrics','metadata':deepcopy(obj['metadata']),'timestamp':now,'window':'30s','containers':[{'name':'api','usage':{'cpu':'80m','memory':'64Mi'}}]})
   return self.reply({'apiVersion':'metrics.k8s.io/v1beta1','kind':'PodMetricsList','metadata':{},'items':items})
  if path.startswith('/apis/metrics.k8s.io/v1beta1/'):
   return self.failure(503,'ServiceUnavailable','Fixture unrelated metrics unavailable')
  if path=='/apis/autoscaling/v2/namespaces/apps/horizontalpodautoscalers':
   hpa={'apiVersion':'autoscaling/v2','kind':'HorizontalPodAutoscaler','metadata':{'name':'payments-scale','namespace':'apps','uid':'fixture-hpa','generation':3},'spec':{'scaleTargetRef':{'apiVersion':'apps/v1','kind':'Deployment','name':'payments-api'},'minReplicas':1,'maxReplicas':4,'metrics':[{'type':'Resource','resource':{'name':'cpu','target':{'type':'Utilization','averageUtilization':60}}}]},'status':{'currentReplicas':2,'desiredReplicas':3,'observedGeneration':3 if self.server.healthy else 2,'currentMetrics':[{'type':'Resource','resource':{'name':'cpu','current':{'averageUtilization':55}}}] if self.server.healthy else [],'conditions':[{'type':'ScalingActive','status':'True' if self.server.healthy else 'False','reason':'ValidMetricFound' if self.server.healthy else 'FailedGetResourceMetric','message':'Fixture metric input available' if self.server.healthy else 'Fixture CPU metric unavailable','lastTransitionTime':'2026-10-04T00:00:00Z'}]}}
   return self.reply({'apiVersion':'autoscaling/v2','kind':'HorizontalPodAutoscalerList','metadata':{},'items':[hpa]})
  return super().do_GET()
 def do_POST(self):self.record('POST');super().do_POST()
 def do_PATCH(self):self.record('PATCH');self.failure(405,'MethodNotAllowed','Read only fixture')
 def do_PUT(self):self.record('PUT');self.failure(405,'MethodNotAllowed','Read only fixture')
 def do_DELETE(self):self.record('DELETE');self.failure(405,'MethodNotAllowed','Read only fixture')

api=j.JourneyAPI();api.RequestHandlerClass=Handler;api.lock=threading.Lock();api.requests=[];api.healthy=False;api.deny_selected=False
api.objects=[o for o in api.objects if o['kind']!='Secret']
for obj in api.objects:
 if obj['kind']=='Pod' and obj['metadata']['name'].startswith('payments-api'):
  obj['metadata']['labels']={'app':'payments'}
  obj['spec']['containers'][0]['resources']={'requests':{'cpu':'1','memory':'512Mi'},'limits':{'cpu':'2','memory':'1Gi'}}
  if obj['status']['phase']=='Pending':
   obj['spec'].pop('nodeName',None)
   obj['status']['conditions']=[{'type':'PodScheduled','status':'False','reason':'Unschedulable','message':'Fixture insufficient memory; inspect placement evidence'}]
api.objects.append({'apiVersion':'v1','kind':'ResourceQuota','metadata':{'name':'team-budget','namespace':'apps','uid':'fixture-quota'},'spec':{'hard':{'requests.cpu':'2','requests.memory':'2Gi'}},'status':{'hard':{'requests.cpu':'2','requests.memory':'2Gi'},'used':{'requests.cpu':'2','requests.memory':'1Gi'}}})
api.objects.append({'apiVersion':'v1','kind':'LimitRange','metadata':{'name':'team-defaults','namespace':'apps','uid':'fixture-limit'},'spec':{'limits':[{'type':'Container','min':{'cpu':'100m'},'defaultRequest':{'cpu':'200m','memory':'128Mi'},'default':{'cpu':'1','memory':'512Mi'}}]}})
threading.Thread(target=api.serve_forever,daemon=True).start();a.output.mkdir(parents=True,exist_ok=True)
demo.COLS,demo.ROWS=120,34
try:
 with tempfile.TemporaryDirectory(prefix='k9plus-capacity-native-') as directory:
  t=demo.Terminal(a.binary.resolve(),directory,api.server_port,command='deployments apps',flags=['--readonly'],ui_config='    noIcons: true\n')
  def frame(name,expected):t.capture(a.output,name,expected)
  def resize(cols,rows):
   fcntl.ioctl(t.master,termios.TIOCSWINSZ,struct.pack('HHHH',rows,cols,0,0));t.screen.resize(rows,cols);demo.COLS,demo.ROWS=cols,rows;os.kill(t.process.pid,signal.SIGWINCH);t.drain(.65)
  try:
   t.drain(4);j.assert_screen(t,['payments-api']);t.command('capacity');t.drain(.7)
   frame('overview-120',['Capacity review / apps/payments-api','partial evidence','1 Pending Pods','Requests: CPU 2','Usage: N/A','team-budget','r refresh','Esc back'])
   for cols,rows in [(80,24),(60,24),(40,16)]:
    resize(cols,rows);frame(f'overview-{cols}x{rows}',['Capacity review','partial evidence','Overview','1 Pending Pods','r refresh','Esc back'])
   t.keys('4',.4);frame('scaling-floor-40x16',['Scaling','AUTOSCALING','r refresh','Esc back'])
   resize(40,12);frame('floor-notice-40x12',['View too small'])
   resize(60,24);frame('scaling-recovered-60',['Scaling','AUTOSCALING','Status stale','Current metric inputs: N/A'])
   resize(80,24);t.keys('3',.4);frame('admission-80',['Admission','Configured hard','remaining 0','LimitRange','default request'])
   t.keys('5',.4);frame('nodes-denied-80',['Nodes','NODE EVIDENCE','denied'])
   t.keys('6',.4);frame('evidence-80',['Evidence','READ-ONLY CAPACITY SNAPSHOT','fixture-deployment','Selector: app=payments','Pods: complete'])
   # Scroll the retained source report: this does not perform another collection.
   t.keys('\x1b[6~',.4);frame('evidence-sources-nodes-80',['Nodes: denied','VPA: absent'])
   t.keys('\x1b[4~',.4);frame('evidence-history-80',['History: not configured'])
   t.keys('\x1b[A'*6,.4);frame('evidence-sources-metrics-80',['Metrics: unavailable','History: not configured'])
   api.healthy=True;t.keys('1r',1.2);frame('overview-refreshed-80',['Usage: CPU 160m','memory 128Mi','2/2 fresh complete Pods'])
   retained=j.text(t).split('Captured ')[1].split('\n')[0]
   api.deny_selected=True;t.keys('r',1.2);frame('failed-refresh-retains-80',['Retained / failed refresh','Previous captured evidence retained','Usage: CPU 160m'])
   if retained not in j.text(t):raise AssertionError('Failed refresh changed original capture time')
   t.keys('\x1b',.5);frame('back-deployments-80',['payments-api','deployments'])
   with api.lock:requests=deepcopy(api.requests)
   mutations=[r for r in requests if r['method']!='GET' and not(r['method']=='POST' and r['path'].startswith('/apis/authorization.k8s.io/'))]
   if mutations:raise AssertionError(f'Mutations attempted: {mutations}')
   if any('/secrets' in r['path'] for r in requests):raise AssertionError('Secret API requested')
   source_reads=[r for r in requests if r['path'] in ['/api/v1/namespaces/apps/pods','/api/v1/nodes','/api/v1/namespaces/apps/resourcequotas','/api/v1/namespaces/apps/limitranges','/apis/autoscaling/v2/namespaces/apps/horizontalpodautoscalers','/apis/autoscaling.k8s.io/v1/namespaces/apps/verticalpodautoscalers','/apis/metrics.k8s.io/v1beta1/namespaces/apps/pods'] and r['query'].get('limit')==['101']]
   if len(source_reads)!=14:raise AssertionError(f'Expected two bounded seven-source collections, got {len(source_reads)}')
   os.write(t.master,b':quit\r');t.process.wait(timeout=5)
   if t.process.returncode!=0:raise AssertionError(f'Normal quit returned {t.process.returncode}')
   (a.output/'api-requests.json').write_text(json.dumps(requests,indent=2)+'\n')
   (a.output/'manifest.json').write_text(json.dumps({'result':'passed','captured_at':datetime.now(timezone.utc).isoformat(),'binary_sha256':hashlib.sha256(a.binary.read_bytes()).hexdigest(),'coverage':'Actual emitted PTY cells; disposable local API fixtures; no live-cluster or operator-study claim.','assertions':['selected Deployment UID and selector pinned','budget versus unavailable/fresh usage','quota remaining zero and configured admission evidence','independent denied Nodes and absent VPA','120 -> 80 -> 60 -> 40x16 -> 40x12 notice -> 60 retained Scaling selection','failed refresh retains original source/time','Back restores selected Deployment','two fixed bounded seven-source collections; no Secret reads or resource mutations','normal keyboard quit'],'captures':t.captures},indent=2)+'\n')
  finally:t.close()
finally:api.stopped.set();api.shutdown();api.server_close()
