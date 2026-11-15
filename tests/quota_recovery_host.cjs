// 隔离原生回归：临时 CPA 配置及本地 mock，不访问真实账户。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {spawn} = require('node:child_process');
const binary = process.env.CPA_HOST_BINARY, library = process.env.CPA_PLUGIN_LIBRARY;
if (!binary || !library) throw Error('Set CPA_HOST_BINARY and CPA_PLUGIN_LIBRARY');
const id = 'commandcode-or-or-cliproxyapi';
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'commandcode-recovery-host-'));
const listen = s => new Promise(r => s.listen(0, '127.0.0.1', r));
const delay = ms => new Promise(r => setTimeout(r, ms));
let spent = true, child, logs = '', modelCalls = 0, browser;
const upstream = http.createServer(async (req, res) => {
  for await (const _ of req) {}
  let body;
  if (req.url.endsWith('/models')) body = {data:[{id:'native-model'}]};
  else if (req.url.includes('/whoami')) body = {success:true,user:{email:'recovery@example.test'}};
  else if (req.url.endsWith('/subscriptions')) body = {success:true,data:{planId:'individual-goat',status:'active',currentPeriodEnd:'2099-01-01T00:00:00Z'}};
  else if (req.url.endsWith('/credits')) body = {credits:{monthlyCredits:50,purchasedCredits:0},windowLimits:{limited:true,fiveHour:{used:0,cap:14},weekly:{used:spent?35:0,cap:35}}};
  else if (req.url.endsWith('/chat/completions')) {modelCalls++;body={id:'local',object:'chat.completion',model:'native-model',choices:[{index:0,message:{role:'assistant',content:'recovered'},finish_reason:'stop'}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}};}
  else {res.writeHead(404);res.end();return;}
  res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(body));
});
(async()=>{
  await listen(upstream);
  const probe=http.createServer();await listen(probe);const port=probe.address().port;await new Promise(r=>probe.close(r));
  const base='http://127.0.0.1:'+port, authDir=path.join(root,'auths');
  fs.mkdirSync(authDir);fs.mkdirSync(path.join(root,'home'));
  const pluginDir=path.join(root,'plugins',process.platform==='win32'?'windows':process.platform,process.arch==='x64'?'amd64':process.arch);
  fs.mkdirSync(pluginDir,{recursive:true});fs.copyFileSync(library,path.join(pluginDir,id+path.extname(library)));
  fs.writeFileSync(path.join(authDir,'a.json'),JSON.stringify({type:'commandcode',api_key:'local-recovery-123',email:'recovery@example.test',custom_preserved:'keep'}));
  const config=path.join(root,'config.yaml');
  fs.writeFileSync(config,['host: 127.0.0.1','port: '+port,'remote-management:','  allow-remote: false','  secret-key: recovery-management','  disable-control-panel: true','auth-dir: '+JSON.stringify(authDir),'api-keys: [recovery-client]','plugins:','  enabled: true','  dir: '+JSON.stringify(path.join(root,'plugins')),'  configs:','    '+id+':','      enabled: true','      allow-http: true','      base-url: http://127.0.0.1:'+upstream.address().port+'/provider/v1',''].join('\n'));
  async function api(url,body,management=true){
    const r=await fetch(base+url,{method:body===undefined?'GET':'POST',headers:{Authorization:'Bearer '+(management?'recovery-management':'recovery-client'),'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(20000)});
    return {status:r.status,data:await r.json().catch(()=>({}))};
  }
  const endpoint='/v0/management/plugins/'+id;
  async function list(){return (await api(endpoint+'/quota-usage',{})).data.cards;}
  async function completion(){return api('/v1/chat/completions',{model:'commandcode/native-model',messages:[{role:'user',content:'local test'}]},false);}
  async function hostEntry(){const r=await api('/v0/management/auth-files');return r.data.files?.find(f=>f.name==='a.json');}
  try {
    child=spawn(binary,['--config',config,'--local-model'],{cwd:root,env:{PATH:process.env.PATH,HOME:path.join(root,'home'),...(process.env.SystemRoot?{SystemRoot:process.env.SystemRoot}:{})},stdio:['ignore','pipe','pipe']});
    child.stdout.on('data',d=>logs+=d);child.stderr.on('data',d=>logs+=d);
    let ready=false;for(let i=0;i<100;i++){try{const cards=await list();if(cards?.length===1){ready=true;break;}}catch{}await delay(100);}assert.ok(ready,'plugin did not register');
    const card=(await list())[0];assert.ok(card.auth_id);
    const blocked=await completion();assert.notEqual(blocked.status,200,JSON.stringify(blocked));assert.equal(modelCalls,0,'spent credential reached model');
    let before=await hostEntry();assert.ok(before.cooldowns?.some(c=>c.reason==='credential_quota'),JSON.stringify(before));
    spent=false;
    const fresh=await api(endpoint+'/quota-usage',{key_id:card.key_id});assert.equal(fresh.status,200);assert.equal(fresh.data.usage.weekly.used,0);assert.match(fresh.data.usage.recovery_notice,/本地旧冷却已撤销/);
    before=await hostEntry();assert.ok(before.cooldowns?.some(c=>c.reason==='credential_quota'),'ordinary refresh cleared host cooldown');
    const unauth=await fetch(base+endpoint+'/cooldown-recovery',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({key_id:card.key_id,auth_id:card.auth_id})});assert.equal(unauth.status,401);
    const preview=await api(endpoint+'/cooldown-recovery',{key_id:card.key_id,auth_id:card.auth_id});
    assert.equal(preview.status,200,JSON.stringify(preview));assert.ok(preview.data.token);
    assert.ok((await hostEntry()).cooldowns.some(c=>c.reason==='credential_quota'),'preview changed routing');
    if(process.env.CPA_BROWSER==='1'){
      const {chromium}=require('playwright');browser=await chromium.launch({headless:true,executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH||undefined});
      const page=await browser.newPage({viewport:{width:375,height:812}}), errors=[];page.on('pageerror',e=>errors.push(e.message));
      await page.addInitScript(()=>localStorage.setItem('cli-proxy-auth',JSON.stringify({state:{managementKey:'recovery-management'}})));
      await page.goto(base+'/v0/resource/plugins/'+id+'/quota');
      page.once('dialog',async d=>{assert.match(d.message(),/所有模型/);await d.dismiss();});
      await page.getByRole('button',{name:'核对并恢复冷却 recovery@example.test',exact:true}).click();
      await page.getByRole('status').filter({hasText:'已取消'}).waitFor();
      assert.ok((await hostEntry()).cooldowns.some(c=>c.reason==='credential_quota'));
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);
      page.once('dialog',async d=>{assert.match(d.message(),/新产生的限制/);await d.accept();});
      await page.getByRole('button',{name:'核对并恢复冷却 recovery@example.test',exact:true}).click();
      await page.getByRole('status').filter({hasText:'宿主已确认'}).waitFor();
      assert.deepEqual(errors,[]);await browser.close();browser=null;
    }else{
      const confirm=await api(endpoint+'/cooldown-recovery',{key_id:card.key_id,auth_id:card.auth_id,token:preview.data.token,confirm:true});assert.equal(confirm.status,200,JSON.stringify(confirm));
      assert.equal((await api(endpoint+'/cooldown-recovery',{key_id:card.key_id,auth_id:card.auth_id,token:preview.data.token,confirm:true})).status,409);
    }
    const after=await hostEntry();assert.equal(after.cooldowns?.length||0,0,JSON.stringify(after));
    const success=await completion();assert.equal(success.status,200,JSON.stringify(success));assert.equal(modelCalls,1);
    const auth=JSON.parse(fs.readFileSync(path.join(authDir,'a.json')));assert.equal(auth.custom_preserved,'keep');assert.notEqual(auth.disabled,true);
    console.log('PASS: real CPA 8.0.69 weekly cooldown -> fresh healthy quota -> ordinary refresh preserves host restriction -> explicit confirmed reset -> actual model routing recovered; browser='+(process.env.CPA_BROWSER==='1')+'. Artifacts: '+root);
  } finally {
    if(browser)await browser.close();if(child&&child.exitCode===null){const done=new Promise(r=>child.once('exit',r));child.kill();await done;}
    fs.writeFileSync(path.join(root,'host.log'),logs);upstream.closeAllConnections();await new Promise(r=>upstream.close(r));
  }
})().catch(e=>{console.error(e);process.exitCode=1;});
