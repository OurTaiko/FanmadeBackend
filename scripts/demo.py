"""Local ESE demonstration import and API smoke tests; source files are read-only."""
import argparse
import hashlib
import http.cookiejar
import io
import json
import os
from pathlib import Path
import secrets
import time
import urllib.error
import urllib.request
import uuid
import zipfile

ORIGIN = os.environ.get('APP_ORIGIN', 'http://127.0.0.1:5173')
BASE = os.environ.get('API_URL', 'http://127.0.0.1:8080') + '/api/v1'
ESE = Path(os.environ.get('ESE_ROOT', str(Path.home() / 'Documents/GitHub/ESE')))
SAMPLES = [
 '03 Vocaloid/Happy Synthesizer/Happy Synthesizer.tja',
 '05 Variety/Destr0yer/Destr0yer.tja',
 '01 Pop/Natsumatsuri/Natsumatsuri New Audio Chart.tja',
 '03 Vocaloid/Godish/Godish.tja',
 '04 Children and Folk/Aiai/Aiai.tja',
 '04 Children and Folk/Silent Night/Silent Night.tja',
]

class Client:
 def __init__(self):
  self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
  self.csrf = ''
 def call(self, method, path, data=None, headers=None):
  h = {'Origin': ORIGIN, 'X-CSRF-Token': self.csrf, **(headers or {})}
  if isinstance(data, dict):
   data = json.dumps(data).encode(); h['Content-Type'] = 'application/json'
  req = urllib.request.Request(BASE + path, data=data, headers=h, method=method)
  try: response = self.opener.open(req, timeout=240)
  except urllib.error.HTTPError as e: response = e
  body = response.read()
  result = json.loads(body) if 'application/json' in response.headers.get('Content-Type', '') else body
  return response.status, result, response.headers
 def auth(self, username, password, register=True):
  status, result, _ = self.call('POST', '/auth/register' if register else '/auth/login', {'username': username, 'password': password})
  if status == 409 and register: return self.auth(username, password, False)
  assert status == 200, (status, result)
  assert result['user']['emailVerified'] is False
  self.csrf = result['csrfToken']; return result['user']
 def upload(self, rel, key=None, name=None, corrupt=False, duplicate=False):
  path = ESE / rel; tja = path.read_bytes()
  wave = next(line.split(':',1)[1].strip() for line in tja.decode('utf-8-sig').splitlines() if line.startswith('WAVE:'))
  audio = (path.parent / wave).read_bytes()
  if name and name.startswith('../'): audio = b'OggS' 
  boundary = 'ourtaiko-' + uuid.uuid4().hex; chunks = []
  def part(field, body, filename=None):
   header = f'--{boundary}\r\nContent-Disposition: form-data; name="{field}"'
   if filename is not None: header += f'; filename="{filename}"'
   chunks.append((header+'\r\n\r\n').encode()+body+b'\r\n')
  part('tja', tja, path.name)
  part('audio', b'OggSbroken' if corrupt else audio, name or wave)
  if duplicate: part('audio', b'OggS', wave)
  part('encoding', b'utf-8')
  part('description', ('本地 ESE 参考谱面演示。原始 TJA 与对应 OGG 经双重校验后导入。\n来源：'+rel).encode())
  chunks.append(f'--{boundary}--\r\n'.encode())
  return self.call('POST', '/charts', b''.join(chunks), {'Content-Type': 'multipart/form-data; boundary='+boundary, 'Idempotency-Key': key or str(uuid.uuid4())})

def seed():
 local = Path('.data'); local.mkdir(exist_ok=True)
 credentials = local / 'demo-credentials.json'
 if credentials.exists(): account = json.loads(credentials.read_text())
 else:
  account = {'username': 'ese_demo', 'password': secrets.token_urlsafe(24)}
  credentials.write_text(json.dumps(account)); credentials.chmod(0o600)
 client = Client(); client.auth(**account)
 imported = []
 for rel in SAMPLES:
  key = hashlib.sha256(('ese-demo-v1:'+rel).encode()).hexdigest()
  status, result, _ = client.upload(rel, key)
  assert status in (200,201), (rel,status,result)
  imported.append({'id':result['id'],'title':result['title'],'source':rel})
  print(f"Imported: {result['title']} ({len(result['difficulties'])} blocks)")
 (local/'demo-imports.json').write_text(json.dumps(imported,ensure_ascii=False,indent=2))
 print('Demo ready. Credentials are saved locally in .data/demo-credentials.json (not tracked).')

def smoke():
 client = Client(); user = client.auth('smoke_'+uuid.uuid4().hex[:10], secrets.token_urlsafe(24))
 other = Client(); other.auth('smoke_'+uuid.uuid4().hex[:10], secrets.token_urlsafe(24))
 rel = SAMPLES[0]
 for duplicate_chart in ['02 Anime/Gekkouka/Gekkouka.tja', '02 Anime/Oto Melody/Oto Melody.tja']:
  status, result, _ = client.upload(duplicate_chart)
  assert status == 422 and result['code'] == 'TJA_DIFFICULTY_DUPLICATE', (status, result)
  assert result['errors'][0]['line'] > 0
 print('PASS duplicate Single difficulties rejected during upload with line numbers')
 for args, expected, code in [({'name':'wrong.ogg'},422,'TJA_AUDIO_MISMATCH'),({'name':'../Happy Synthesizer.ogg'},400,'UPLOAD_FILES_INVALID'),({'corrupt':True},422,'AUDIO_INVALID'),({'duplicate':True},400,'UPLOAD_FILES_INVALID')]:
  status, result, _ = client.upload(rel,**args)
  assert status == expected and result['code'] == code, (status,result)
  print('PASS', code)
 status, result, _ = client.call('GET','/me/charts')
 assert status == 200 and result['total'] == 0
 key = str(uuid.uuid4()); status, chart, _ = client.upload(rel,key)
 assert status == 201, (status,chart)
 try:
  status, repeated, _ = client.upload(rel,key)
  assert status == 200 and repeated['id'] == chart['id']
  status, conflict, _ = client.upload(SAMPLES[5],key)
  assert status == 409 and conflict['code'] == 'IDEMPOTENCY_CONFLICT'
  assert len(chart['difficulties']) == 5 and chart['ownerId'] == user['id']
  assert all(d['style'] == 'Single' and d['cloudScoreEligible'] is True for d in chart['difficulties'])
  suffix=f"/charts/{chart['id']}/versions/{chart['versionId']}"
  status, archive, _ = client.call('GET',suffix+'/download'); assert status == 200
  z=zipfile.ZipFile(io.BytesIO(archive)); original=ESE/rel
  assert z.read(original.name) == original.read_bytes()
  assert z.read(chart['wave']) == (original.parent/chart['wave']).read_bytes()
  status, part, headers = client.call('GET',suffix+'/audio',headers={'Range':'bytes=0-3'})
  assert status == 206 and part == b'OggS' and headers.get('Content-Range')
  status, _, _ = other.call('DELETE','/charts/'+chart['id'],{})
  assert status == 403
  status, _, _ = client.call('DELETE','/charts/'+chart['id'],{},headers={'X-CSRF-Token':''})
  assert status == 403
  print('PASS upload, parsed metadata, idempotency, ZIP byte equality, audio Range, ownership and CSRF')
 finally:
  status, _, _ = client.call('DELETE','/charts/'+chart['id'],{}); assert status == 200
 status, _, _ = client.call('GET',suffix+'/download'); assert status == 404
 # A mixed ESE file remains downloadable; only its Single blocks qualify.
 status, mixed, _ = client.upload(SAMPLES[2])
 assert status == 201, (status, mixed)
 try:
  blocks = mixed['difficulties']
  assert any(d['style'] == 'Single' and d['cloudScoreEligible'] for d in blocks)
  # Easy omits STYLE after the previous course's Double section.
  assert all(d['style'] == 'Single' and d['cloudScoreEligible'] for d in blocks if d['course'] == 'Easy')
  assert any(d['player'] == 'P1' for d in blocks) and any(d['player'] == 'P2' for d in blocks)
  assert all(d['style'] == 'Double' and d['cloudScoreEligible'] is False for d in blocks if d['player'])
  status, detail, _ = client.call('GET', '/charts/'+mixed['id'])
  assert status == 200 and detail['difficulties'] == blocks
  status, listed, _ = client.call('GET', '/me/charts')
  assert status == 200 and next(c for c in listed['items'] if c['id'] == mixed['id'])['difficulties'] == blocks
  status, archive, _ = client.call('GET', f"/charts/{mixed['id']}/versions/{mixed['versionId']}/download")
  assert status == 200
  original = ESE / SAMPLES[2]
  with zipfile.ZipFile(io.BytesIO(archive)) as z:
   assert z.read(original.name) == original.read_bytes()
   assert z.read(mixed['wave']) == (original.parent/mixed['wave']).read_bytes()
  print('PASS mixed Single/Double eligibility on upload, detail and list; original ZIP preserved')
 finally:
  status, _, _ = client.call('DELETE','/charts/'+mixed['id'],{}); assert status == 200
 status, _, _ = client.call('POST','/auth/logout',{}); assert status == 200
 status, result, _ = client.call('GET','/me'); assert result['user'] is None
 print('PASS deletion revokes downloads and logout revokes session')

if __name__=='__main__':
 parser=argparse.ArgumentParser();parser.add_argument('mode',choices=['seed','test']);args=parser.parse_args()
 seed() if args.mode=='seed' else smoke()
