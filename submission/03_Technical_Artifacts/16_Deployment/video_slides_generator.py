import re,base64,json
R="/home/user/voltsight-connected-vehicle-intelligence"
SH=R+"/submission/03_Technical_Artifacts/15_Screenshots"
doc=open('/tmp/doc/solution.html').read()
m=re.search(r'<svg viewBox="0 0 860.*?</svg>',doc,re.S); arch=m.group(0)
b64=lambda p: base64.b64encode(open(p,'rb').read()).decode()
arch2=arch.replace('<svg ','<svg style="height:624px;width:auto" ',1)
cnt=json.load(open('/tmp/doc/counts.json'))['counts']
sq=open(R+"/evidence/G9b/plans/sqlopt.txt").read().split("\n")
pl=lambda a,b:"\n".join(x[:100] for x in sq[a-1:b])
css='''*{box-sizing:border-box}body{margin:0;background:#222}
.sl{width:1280px;height:720px;background:#0f1b2d;color:#fff;font-family:Helvetica,Arial,sans-serif;position:relative;overflow:hidden;margin-bottom:10px}
.sl h2{font-size:34px;margin:0;padding:34px 48px 0;font-weight:700}.sl h2 span{color:#35c493}
.logo{position:absolute;right:40px;top:30px;font-size:18px;color:#35c493;font-weight:700}
.sub{padding:6px 48px;color:#aab6c6;font-size:18px}
.card{background:#16263d;border:1px solid #2a3f5e;border-radius:10px;padding:18px 22px}
.g{display:grid;gap:18px;padding:18px 48px}
.big{font-size:54px;font-weight:800}.pass{color:#35c493}.part{color:#f0b429}.blk{color:#a78bfa}.fail{color:#ff6b6b}
li{margin:9px 0;font-size:21px;line-height:1.35}pre{font:11px/1.3 Menlo,monospace;color:#cfe3ff;margin:0;white-space:pre}
table{border-collapse:collapse;width:100%;font-size:18px}td{padding:7px 10px;border-bottom:1px solid #2a3f5e}'''
s2=f'''<div class="sl" id="s2"><div class="logo">⚡ VoltSight</div><h2>The problem: <span>range is not usable range</span></h2>
<div class="g" style="grid-template-columns:1fr 1fr;margin-top:10px"><div class="card"><ul>
<li><b>Range uncertainty</b> — varies by vehicle, route, driver</li><li><b>Battery degradation</b> — state of health is not reported</li><li><b>Temperature, load, route</b> change energy per km</li><li><b>Charger availability</b> — the nearest charger may be down</li><li><b>Charging cost</b> — time-of-use tariffs, depot congestion</li><li><b>Fleet scale and latency</b> — decisions must arrive in seconds</li></ul></div>
<div class="card"><div style="font-size:20px;color:#aab6c6">Official challenge scale</div><div class="big pass" style="margin:6px 0">100,000</div><div style="font-size:20px">vehicles</div><div class="big pass" style="margin:14px 0 6px">100,000+</div><div style="font-size:20px">events per second, 3× burst, no data loss</div><div class="big pass" style="margin:14px 0 6px">&lt; 5 s</div><div style="font-size:20px">critical alert latency</div></div></div></div>'''
s3=f'''<div class="sl" id="s3" style="background:#fff;color:#14213d"><div class="logo" style="color:#0b6e4f">⚡ VoltSight</div><h2 style="color:#14213d;padding-top:18px;font-size:28px">Architecture: <span style="color:#0b6e4f">every box is a service in the repository</span></h2><div style="padding:0;height:632px;text-align:center">{arch2}</div></div>'''
s8=f'''<div class="sl" id="s8"><div class="logo">⚡ VoltSight</div><h2>Engineering depth: <span>measured, not claimed</span></h2>
<div class="g" style="grid-template-columns:1.25fr 1.15fr 0.6fr;margin-top:8px">
<div class="card"><div style="font-size:16px;color:#aab6c6;margin-bottom:6px">Prometheus scrape targets (gateway, worker, keycloak, otel-collector): all up</div><img src="data:image/png;base64,{b64('/tmp/vid/prom_crop.png')}" style="width:100%;border-radius:6px"/></div>
<div class="card"><div style="font-size:16px;color:#aab6c6;margin-bottom:6px">EXPLAIN ANALYZE: alert inbox, before → after</div><pre>{pl(22,36).replace('<','&lt;')}</pre><div style="font-size:15px;color:#35c493;margin:6px 0 4px">AFTER: keyset + partial index</div><pre>{pl(61,70).replace('<','&lt;')}</pre></div>
<div class="card"><div style="font-size:16px;color:#aab6c6">OWASP ZAP 2.17.0 baseline</div><div class="big pass" style="font-size:34px;margin-top:8px">58 pass</div><div class="big fail" style="font-size:34px">0 fail</div><div class="big part" style="font-size:34px">3 warning groups</div><div style="font-size:14px;color:#aab6c6;margin-top:8px">unauthenticated scan; warnings documented, not hidden</div></div></div></div>'''
s9=f'''<div class="sl" id="s9"><div class="logo">⚡ VoltSight</div><h2>Requirement matrix: <span>honest statuses</span></h2>
<div class="g" style="grid-template-columns:repeat(4,1fr);padding-bottom:6px"><div class="card"><div class="big pass">{cnt['PASS']}</div>PASS</div><div class="card"><div class="big part">{cnt['PARTIAL']}</div>PARTIAL</div><div class="card"><div class="big blk">{cnt['BLOCKED']}</div>BLOCKED</div><div class="card"><div class="big fail">{cnt['FAIL']}</div>FAIL</div></div>
<div class="g" style="padding-top:0"><div class="card"><table>
<tr><td>Events/s (100K vehicles, 60 s)</td><td>target 100,000+</td><td><b>98.3K measured</b></td><td class="part">PARTIAL</td></tr>
<tr><td>3× burst / 5 chaos scenarios</td><td>no data loss</td><td><b>0 lost</b> (burst 40 s)</td><td class="part">PARTIAL</td></tr>
<tr><td>API p95 / p99</td><td>&lt; 200 / &lt; 500 ms</td><td><b>26.8 / 41.0 ms</b></td><td class="pass">PASS</td></tr>
<tr><td>Critical alert latency</td><td>&lt; 5 s</td><td><b>p99 17.0 s</b> at 100K ev/s, one host</td><td class="fail">FAIL</td></tr>
<tr><td>Soak, scale-out, BDD, production cloud</td><td>required</td><td><b>not run</b></td><td class="part">PARTIAL / FAIL</td></tr></table></div></div></div>'''
s11='''<div class="sl" id="s11" style="display:flex;flex-direction:column;justify-content:center;align-items:center;text-align:center"><div style="font-size:76px;font-weight:800">⚡ VoltSight</div><div style="font-size:30px;color:#35c493;margin:8px 0 28px">EV Fleet Range-Risk &amp; Charge Orchestration</div><div style="font-size:22px;color:#cfe3ff">github.com/SohamB4746Y/voltsight-connected-vehicle-intelligence</div><div style="font-size:18px;color:#aab6c6;margin-top:14px">Live demo: GitHub Codespaces (URL and sign-in in the submission manifest) · All data synthetic</div></div>'''
open('slides.html','w').write(f'<!doctype html><html><head><meta charset="utf-8"><style>{css}</style></head><body>{s2}{s3}{s8}{s9}{s11}</body></html>')
