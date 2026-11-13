// 隔离原生集成：只使用临时文件及本机 mock 上游/代理，不访问真实凭据。
const assert=require('node:assert/strict');
const fs=require('node:fs');
const os=require('node:os');
const path=require('node:path');
const http=require('node:http');
const net=require('node:net');
const {spawn}=require('node:child_process');
const {createHash}=require('node:crypto');
const binary=process.env.CPA_HOST_BINARY, library=process.env.CPA_PLUGIN_LIBRARY;
if(!binary||!library)throw Error('Set CPA_HOST_BINARY and CPA_PLUGIN_LIBRARY');
const id='commandcode-or-or-cliproxyapi', mgmt='/v0/management', root=fs.mkdtempSync(path.join(os.tmpdir(),'commandcode-proxy-host-'));
const poolPath='/v8/management/config/plugins/configs/'+id+'/proxy-pool';
const parentPath='/v8/management/config/plugins/configs/'+id;
const hash=v=>createHash('sha256').update(v).digest('hex');
let child, logs='', lastExit='direct', browser;
const hits=[];
const listen=s=>new Promise(r=>s.listen(0,'127.0.0.1',r));
const delay=ms=>new Promise(r=>setTimeout(r,ms));
async function until(fn,label){for(let i=0;i<100;i++){if(await fn().catch(()=>false))return;await delay(100);}throw Error(label);}
const upstream=http.createServer(async(req,res)=>{
  let raw='';for await(const chunk of req)raw+=chunk;
  const key=(req.headers.authorization||'').replace(/^Bearer /,'');
  lastExit=req.headers['x-test-proxy']||'direct';hits.push({url:req.url,key,exit:lastExit});
  const reply=data=>{res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(data));};
  if(req.url.endsWith('/models'))return reply({data:[{id:'native-model'}]});
  if(req.url.endsWith('/whoami'))return reply({success:true,user:{email:'proxy@example.test'}});
  if(req.url.endsWith('/subscriptions'))return reply({success:true,data:{planId:'individual-goat',status:'active',currentPeriodEnd:'2099-01-01T00:00:00Z'}});
  if(req.url.endsWith('/credits'))return reply({credits:{monthlyCredits:999,purchasedCredits:0},windowLimits:{limited:true,fiveHour:{used:0,cap:100},weekly:{used:0,cap:500}}});
  if(req.url.endsWith('/chat/completions')){
    const request=JSON.parse(raw);
    if(request.stream){
      res.writeHead(200,{'Content-Type':'text/event-stream'});
      res.write('data: '+JSON.stringify({id:'mock',model:'native-model',choices:[{index:0,delta:{role:'assistant',content:'via-'+lastExit},finish_reason:null}]})+'\n\n');
      res.end('data: '+JSON.stringify({id:'mock',model:'native-model',choices:[{index:0,delta:{},finish_reason:'stop'}]})+'\n\ndata: [DONE]\n\n');return;
    }
    return reply({id:'mock',object:'chat.completion',model:'native-model',choices:[{index:0,message:{role:'assistant',content:'via-'+lastExit},finish_reason:'stop'}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}});
  }
  res.writeHead(404);res.end();
});
function proxy(name){return http.createServer((req,res)=>{
  let u;try{u=new URL(req.url,'http://127.0.0.1:'+upstream.address().port);}catch{res.writeHead(400);res.end();return;}
  if(u.hostname!=='127.0.0.1'||Number(u.port)!==upstream.address().port){res.writeHead(403);res.end();return;}
  const headers={...req.headers,'x-test-proxy':name};delete headers['proxy-authorization'];
  const out=http.request(u,{method:req.method,headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res);});
  out.on('error',()=>{res.writeHead(502);res.end();});req.pipe(out);
});}
const p1=proxy('one'),p2=proxy('two');
// SOCKS5 mock 只允许连接测试上游；转到 one 代理以在响应中证明出口。
let socksConnections=0;
const socksSockets=new Set();
const socks=net.createServer(socket=>{
  socksSockets.add(socket);socket.on('close',()=>socksSockets.delete(socket));socket.on('error',()=>{});
  let buffer=Buffer.alloc(0),stage=0;
  function receive(chunk){
    buffer=Buffer.concat([buffer,chunk]);
    if(stage===0){
      if(buffer.length<2||buffer.length<2+buffer[1])return;
      if(buffer[0]!==5||!buffer.subarray(2,2+buffer[1]).includes(0)){socket.end(Buffer.from([5,255]));return;}
      buffer=buffer.subarray(2+buffer[1]);stage=1;socket.write(Buffer.from([5,0]));
    }
    if(stage!==1||buffer.length<4)return;
    let offset,host;
    if(buffer[3]===1){offset=8;if(buffer.length<10)return;host=Array.from(buffer.subarray(4,8)).join('.');}
    else if(buffer[3]===3){if(buffer.length<5)return;offset=5+buffer[4];if(buffer.length<offset+2)return;host=buffer.subarray(5,offset).toString();}
    else {socket.destroy();return;}
    const port=buffer.readUInt16BE(offset);
    if(buffer[0]!==5||buffer[1]!==1||host!=='127.0.0.1'||port!==upstream.address().port){socket.destroy();return;}
    stage=2;socket.removeListener('data',receive);
    const tail=buffer.subarray(offset+2);
    const remote=net.connect(p1.address().port,'127.0.0.1',()=>{
      socksConnections++;socket.write(Buffer.from([5,0,0,1,127,0,0,1,0,0]));
      if(tail.length)remote.write(tail);socket.pipe(remote);remote.pipe(socket);
    });
    socksSockets.add(remote);remote.on('close',()=>socksSockets.delete(remote));
    remote.on('error',()=>socket.destroy());socket.on('close',()=>remote.destroy());
  }
  socket.on('data',receive);
});
(async()=>{
 await listen(upstream);await listen(p1);await listen(p2);await listen(socks);
 const probe=http.createServer();await listen(probe);const port=probe.address().port;await new Promise(r=>probe.close(r));
 const base='http://127.0.0.1:'+port, authDir=path.join(root,'auths');
 const pluginDir=path.join(root,'plugins',process.platform==='win32'?'windows':process.platform,process.arch==='x64'?'amd64':process.arch);
 fs.mkdirSync(authDir);fs.mkdirSync(pluginDir,{recursive:true});fs.mkdirSync(path.join(root,'home'));
 fs.copyFileSync(library,path.join(pluginDir,id+path.extname(library)));
 fs.writeFileSync(path.join(authDir,'a.json'),JSON.stringify({type:'commandcode',api_key:'native-proxy-a',email:'a@example.test',custom_preserved:'keep-a'}));
 fs.writeFileSync(path.join(authDir,'b.json'),JSON.stringify({type:'commandcode',api_key:'native-proxy-b',email:'b@example.test',custom_preserved:'keep-b'}));
 const config=path.join(root,'config.yaml');
 fs.writeFileSync(config,['host: 127.0.0.1','port: '+port,'remote-management:','  allow-remote: false','  secret-key: proxy-test-management','  disable-control-panel: true','auth-dir: '+JSON.stringify(authDir),'api-keys: [proxy-test-client]','plugins:','  enabled: true','  dir: '+JSON.stringify(path.join(root,'plugins')),'  configs:','    '+id+':','      enabled: true','      allow-http: true','      base-url: http://127.0.0.1:'+upstream.address().port+'/provider/v1',''].join('\n'));
 async function api(url,method='GET',body,headers={}){
   const r=await fetch(base+url,{method,headers:{Authorization:'Bearer proxy-test-management','Content-Type':'application/json',...headers},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(20000)});
   return {status:r.status,etag:r.headers.get('etag'),data:await r.json().catch(()=>({}))};
 }
 function start(){child=spawn(binary,['--config',config,'--local-model'],{cwd:root,env:{PATH:process.env.PATH,HOME:path.join(root,'home'),LANG:'en_US.UTF-8',...(process.env.SystemRoot?{SystemRoot:process.env.SystemRoot}:{})},stdio:['ignore','pipe','pipe']});child.stdout.on('data',d=>logs+=d);child.stderr.on('data',d=>logs+=d);}
 async function stop(){if(!child||child.exitCode!==null)return;const done=new Promise(r=>child.once('exit',r));child.kill('SIGTERM');await Promise.race([done,delay(8000).then(()=>{throw Error('host stop timeout');})]);}
 async function ready(){
   await until(async()=>{const r=await api(mgmt+'/plugins/'+id+'/quota-usage','POST',{});return r.status===200&&r.data.cards?.length===2;},'plugin did not register');
   await until(async()=>{const r=await fetch(base+'/v1/models',{headers:{Authorization:'Bearer proxy-test-client'},signal:AbortSignal.timeout(10000)});return r.ok&&(await r.json()).data?.some(m=>m.id==='commandcode/native-model');},'native model was not published');
 }
 async function setEnabled(name,enabled){const r=await api(mgmt+'/auth-files/status','PATCH',{name,disabled:!enabled});assert.equal(r.status,200,JSON.stringify(r));}
 async function completion(exit,stream=false){const r=await fetch(base+'/v1/chat/completions',{method:'POST',headers:{Authorization:'Bearer proxy-test-client','Content-Type':'application/json'},body:JSON.stringify({model:'commandcode/native-model',messages:[{role:'user',content:'test proxy egress'}],stream}),signal:AbortSignal.timeout(20000)});const text=await r.text();assert.equal(r.status,200,text);assert.match(text,new RegExp('via-'+exit));assert.equal(lastExit,exit);}
 const url1='http://127.0.0.1:'+p1.address().port,url2='http://127.0.0.1:'+p2.address().port;
 const entries=[{id:'px_'+'1'.repeat(32),label:'代理一',url:url1},{id:'px_'+'2'.repeat(32),label:'代理二',url:url2}];
 try{
   start();await ready();
   const validation=await api(mgmt+'/plugins/'+id+'/proxy-pool/validate','POST',{lines:url1+'\n'+url2,existing:[]});assert.equal(validation.status,200);assert.equal(validation.data.valid,2);
   const parent=await api(parentPath);assert.equal(parent.status,200);assert.ok(parent.etag);
   const saved=await api(poolPath,'PUT',{'schema-version':1,proxies:entries},{'If-Match':parent.etag});assert.equal(saved.status,200,JSON.stringify(saved));
   assert.equal((await api(poolPath,'PUT',{'schema-version':1,proxies:[]},{'If-Match':parent.etag})).status,412,'stale config write was not rejected');
   for(const [name,p] of [['a.json',entries[0]],['b.json',entries[1]]]){
     const r=await api(mgmt+'/auth-files/fields','PATCH',{name,commandcode_proxy_id:p.id,proxy_url:p.url});assert.equal(r.status,200,JSON.stringify(r));
     const actual=JSON.parse(fs.readFileSync(path.join(authDir,name)));assert.equal(actual.proxy_url,p.url);assert.equal(actual.commandcode_proxy_id,p.id);assert.match(actual.custom_preserved,/keep-/);assert.match(actual.api_key,/native-proxy-/);
   }
   const snapshot=await api(mgmt+'/plugins/'+id+'/quota-usage','POST',{});
   for(const [name,p]of [['a.json',entries[0]],['b.json',entries[1]]]){const c=snapshot.data.cards.find(c=>c.file_name===name);assert.equal(c.proxy_id,p.id);assert.equal(c.proxy_url_hash,hash(p.url));assert.equal(c.proxy_read_error,false);}
   await setEnabled('a.json',true);await setEnabled('b.json',false);await completion('one');await completion('one',true);
   await setEnabled('a.json',false);await setEnabled('b.json',true);await completion('two');await completion('two',true);
   // 额度查询不继承账号代理，必须与界面上的能力边界一致。
   const list=await api(mgmt+'/plugins/'+id+'/quota-usage','POST',{}), b=list.data.cards.find(c=>c.file_name==='b.json');const n=hits.length;
   assert.equal((await api(mgmt+'/plugins/'+id+'/quota-usage','POST',{key_id:b.key_id})).status,200);
   assert.ok(hits.slice(n).some(h=>h.url.endsWith('/credits')));assert.ok(hits.slice(n).every(h=>h.exit==='direct'));
   // 管理写入与运行时热更新异步；重启后再检查优先级，避免测到旧配置。
   assert.equal((await api(mgmt+'/proxy-url','PUT',{value:url1})).status,200);
   await stop();start();await ready();
   assert.equal((await api(mgmt+'/proxy-url')).data['proxy-url'],url1);
   await completion('one');await completion('one',true);
   assert.equal((await api(mgmt+'/proxy-url','PUT',{value:''})).status,200);
   await stop();start();await ready();await completion('two');
   assert.deepEqual((await api(poolPath)).data,{'schema-version':1,proxies:entries});
   // 清空原生 proxy_url 必须用空字符串，不能以 null 代替。
   assert.equal((await api(mgmt+'/auth-files/fields','PATCH',{name:'b.json',commandcode_proxy_id:null,proxy_url:''})).status,200);
   await completion('direct');
   const unbound=await api(mgmt+'/plugins/'+id+'/quota-usage','POST',{});const u=unbound.data.cards.find(c=>c.file_name==='b.json');assert.equal(u.proxy_id,'');assert.equal(u.proxy_configured,false);
   // 验证 SOCKS5 通路；mock 仅允许通往测试上游并转发到 HTTP proxy one。
   const socksURL='socks5://127.0.0.1:'+socks.address().port,socksId='px_'+'3'.repeat(32),dir=await api(poolPath);
   const socksPool=[...entries,{id:socksId,label:'SOCKS 代理',url:socksURL}];
   assert.equal((await api(poolPath,'PUT',{'schema-version':1,proxies:socksPool},{'If-Match':dir.etag})).status,200);
   assert.equal((await api(mgmt+'/auth-files/fields','PATCH',{name:'b.json',commandcode_proxy_id:socksId,proxy_url:socksURL})).status,200);
   await stop();start();await ready();await completion('one');await completion('one',true);assert.ok(socksConnections>0,'SOCKS5 mock was not used');
   if(process.env.CPA_BROWSER==='1'){
     const {chromium}=require('playwright');browser=await chromium.launch({headless:true,executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH||undefined});
     const page=await browser.newPage({viewport:{width:1100,height:850}}), errors=[];page.on('pageerror',e=>errors.push(e.message));
     await page.addInitScript(()=>localStorage.setItem('cli-proxy-auth',JSON.stringify({state:{managementKey:'proxy-test-management'}})));
     await page.goto(base+'/v0/resource/plugins/'+id+'/quota');await page.locator('#proxy-panel').evaluate(el=>el.open=true);
     await page.waitForFunction(()=>!document.querySelector('#proxy-validate').disabled);assert.equal(await page.locator('#proxy-count').innerText(),'3 个代理');
     const card=page.locator('.quota-card').filter({hasText:'b.json'});await card.locator('select').selectOption(entries[1].id);await card.getByRole('button',{name:'预览代理设置'}).click();await page.locator('#proxy-plan-apply').click();
     await page.waitForFunction(()=>document.querySelector('#proxy-bind-status').textContent.includes('失败或未知 0'));
     await completion('two');assert.deepEqual(errors,[]);await browser.close();browser=null;
   }
   console.log('PASS: real CPA 8 native plugin proxy directory CAS + auth field persistence + HTTP account unary/stream exits + default quota exit + global override + restart + empty-string unbind + browser='+(process.env.CPA_BROWSER==='1')+'. Artifacts: '+root);
 }finally{
   if(browser)await browser.close();await stop();fs.writeFileSync(path.join(root,'host.log'),logs);
   for(const s of socksSockets)s.destroy();await new Promise(r=>socks.close(r));
   for(const s of [upstream,p1,p2]){s.closeAllConnections();await new Promise(r=>s.close(r));}
 }
})().catch(e=>{console.error('Artifacts: '+root);console.error(e.stack);process.exitCode=1;});
