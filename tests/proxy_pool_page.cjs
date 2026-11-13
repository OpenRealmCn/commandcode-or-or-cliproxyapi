// 本地 mock 浏览器回归，不访问真实代理或凭据。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const {createHash} = require('node:crypto');
const {chromium} = require('playwright');
const html = fs.readFileSync(path.join(__dirname, '../resources/quota_page.html'), 'utf8');
const id = 'commandcode-or-or-cliproxyapi';
const baseConfig = '/v8/management/config/plugins/configs/' + id;
const hash = value => createHash('sha256').update(value).digest('hex');
const oldProxy = {id:'px_'+'1'.repeat(32),label:'已有代理',url:'http://old.example:8080'};
let pool = {'schema-version':1,proxies:[oldProxy]};
let revision = 1, conflict = false, puts = 0, failPatch = '', uncertainPatch = '', fieldSupport = true;
let unknownAccount = false, withGlobal = false;
const patches = [];
const defaults = {proxy_id:'',proxy_configured:false,proxy_read_error:false,proxy_url_hash:'',proxy_display:''};
let cards = [
  {...defaults,key_id:'a',file_name:'a.json',label:'a@example.test',email:'a@example.test'},
  {...defaults,key_id:'b',file_name:'b.json',label:'b@example.test',email:'b@example.test'},
  {...defaults,key_id:'c',file_name:'c.json',label:'c@example.test',email:'c@example.test',proxy_id:oldProxy.id,proxy_configured:true,proxy_url_hash:hash(oldProxy.url),proxy_display:oldProxy.url},
  {...defaults,key_id:'d',file_name:'d.json',label:'d@example.test',email:'d@example.test',proxy_configured:true,proxy_url_hash:hash('http://external.example:80'),proxy_display:'http://external.example:80'},
];
function validate(body) {
  const known = new Map((body.existing||[]).map(p=>[p.url,p.id])), seen = new Set();
  const rows = [];
  String(body.lines||'').split('\n').forEach((line,i)=>{
    if (!line.trim()) return;
    try {
      if (!line.includes('://') && body.default_protocol) line = body.default_protocol+'://'+line;
      const u = new URL(line);
      if (!['http:','https:','socks5:','socks5h:'].includes(u.protocol) || !u.port || u.search || u.hash || (u.pathname && u.pathname!=='/') || /\s/.test(line)) throw new Error();
      const url = u.protocol+'//'+(u.username ? u.username+(u.password?':'+u.password:'')+'@':'')+u.host;
      const display = u.protocol+'//'+(u.username?'***:***@':'')+u.host;
      const duplicate = known.has(url)||seen.has(url); seen.add(url);
      rows.push({line:i+1,status:duplicate?'duplicate':'valid',...(duplicate?{}:{url}),url_hash:hash(url),display,...(known.has(url)?{duplicate_id:known.get(url)}:{})});
    } catch {rows.push({line:i+1,status:'error',error:'代理地址格式无效'});}
  });
  return {rows,valid:rows.filter(r=>r.status==='valid').length,duplicates:rows.filter(r=>r.status==='duplicate').length,errors:rows.filter(r=>r.status==='error').length};
}
const server = http.createServer(async(req,res)=>{
  const reply=(data,code=200,etag=false)=>{res.writeHead(code,{'Content-Type':'application/json',...(etag?{ETag:'"'+revision+'"'}:{})});res.end(JSON.stringify(data));};
  if(req.url==='/'){res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html);return;}
  if(req.headers.authorization!=='Bearer proxy-test-management') return reply({error:'unauthorized'},401);
  let raw=''; for await(const chunk of req)raw+=chunk;
  const body=raw?JSON.parse(raw):{};
  if(req.url===baseConfig+'/proxy-pool') {
    if(req.method==='GET')return pool?reply(pool,200,true):reply({error:'not_found'},404);
    if(req.method==='PUT') {
      puts++;
      if(conflict){conflict=false;revision++;return reply({error:'conflict with secret-do-not-echo'},412);}
      if(req.headers['if-match']!=='"'+revision+'"')return reply({},412);
      pool=body;revision++;return reply({status:'ok'},200,true);
    }
  }
  if(req.url===baseConfig)return reply({enabled:true},200,true);
  if(req.url.endsWith('/proxy-pool/validate'))return reply(validate(body));
  if(req.url==='/v0/management/proxy-url')return reply({'proxy-url':withGlobal?'http://global-secret-user:global-secret-pass@proxy:80':''});
  if(req.url.endsWith('/quota-usage'))return reply({cards:cards.map(c=>({...c,proxy_read_error:unknownAccount||c.proxy_read_error}))});
  if(req.url==='/v0/management/auth-files/fields') {
    if(!fieldSupport)return reply({},405);
    patches.push({...body});
    if(failPatch===body.name){failPatch='';return reply({error:'secret-do-not-echo'},500);}
    const c=cards.find(c=>c.file_name===body.name);
    if(!c)return reply({},404);
    Object.assign(c,{proxy_id:body.commandcode_proxy_id||'',proxy_configured:!!body.proxy_url,proxy_url_hash:body.proxy_url?hash(body.proxy_url):'',proxy_display:body.proxy_url?body.proxy_url.replace(/\/\/[^@]+@/,'//***:***@'):''});
    if(uncertainPatch===body.name){uncertainPatch='';return reply({error:'response lost after commit'},500);}
    return reply({status:'ok'});
  }
  reply({},404);
});
(async()=>{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));
  let browser;
  try {
    browser=await chromium.launch({headless:true,executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH||undefined});
    const page=await browser.newPage({viewport:{width:1200,height:900}}), errors=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('dialog',d=>d.accept(d.type()==='prompt'?'更名代理':undefined));
    await page.addInitScript(()=>localStorage.setItem('cli-proxy-auth',JSON.stringify({state:{managementKey:'proxy-test-management'}})));
    const reload=async()=>{await page.goto('http://127.0.0.1:'+server.address().port);await page.locator('#proxy-panel').evaluate(el=>el.open=true);await page.waitForFunction(()=>!document.querySelector('#proxy-validate').disabled);};
    await reload();
    assert.equal(await page.locator('.quota-card').count(),4);
    assert.match(await page.locator('.quota-card').nth(3).innerText(),/外部设置/);
    assert.equal(await page.locator('#proxy-items button').filter({hasText:'删除'}).isDisabled(),true);
    // 混合导入去重、错误预览、秘密不会显示到列表或浏览器持久存储。
    await page.locator('#proxy-lines').fill('socks5h://alice:proxy-test-password@proxy.example:1080\nsocks5h://alice:proxy-test-password@proxy.example:1080\nnot-a-uri');
    await page.locator('#proxy-validate').click();
    await page.waitForFunction(()=>!document.querySelector('#proxy-import').disabled);
    assert.match(await page.locator('#proxy-preview-status').innerText(),/有效 1，重复 1，错误 1/);
    assert.equal((await page.locator('#proxy-preview').innerText()).includes('proxy-test-password'),false);
    await page.locator('#proxy-import').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-count').textContent==='2 个代理'&&!document.querySelector('#proxy-validate').disabled);
    assert.equal(pool.proxies.length,2);assert.match(pool.proxies[1].id,/^px_[0-9a-f]{32}$/);
    assert.equal(pool.proxies[1].url,'socks5h://alice:proxy-test-password@proxy.example:1080');
    assert.equal((await page.locator('#proxy-items').innerText()).includes('proxy-test-password'),false);
    assert.equal(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}).includes('proxy-test-password')),false);
    // 随机仅处理未绑定账号，不覆盖已绑定 c 或外部 d。
    await page.locator('#proxy-random').click();
    const preview=await page.locator('#proxy-plan-list').innerText();assert.match(preview,/a.json/);assert.match(preview,/b.json/);assert.doesNotMatch(preview,/c.json|d.json/);
    failPatch='b.json';
    await page.locator('#proxy-plan-apply').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-bind-status').textContent.includes('失败或未知 1'));
    assert.equal(patches.length,2);const mapping=patches.map(p=>({name:p.name,id:p.commandcode_proxy_id,url:p.proxy_url}));
    assert.equal(patches.some(p=>['c.json','d.json'].includes(p.name)),false);
    await page.locator('#proxy-retry').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-bind-status').textContent.includes('失败或未知 0'));
    assert.equal(patches.length,3);assert.deepEqual({name:patches[2].name,id:patches[2].commandcode_proxy_id,url:patches[2].proxy_url},mapping[1]);
    // 保存后返回 500 的结果回读成功，不能再次随机或重复 PATCH。
    await page.locator('.quota-card').nth(0).locator('select').selectOption('');
    await page.locator('.quota-card').nth(0).getByRole('button',{name:'预览代理设置'}).click();
    uncertainPatch='a.json';await page.locator('#proxy-plan-apply').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-bind-status').textContent.includes('失败或未知 0')&&document.querySelector('#proxy-plan').hidden);
    assert.equal(cards[0].proxy_configured,false);assert.equal(patches.at(-1).proxy_url,'');assert.equal(patches.at(-1).commandcode_proxy_id,null);
    // 预览后另一个页面改了账号，确认时不覆盖。
    await page.locator('#proxy-random').click();
    Object.assign(cards[0],{proxy_id:'px_'+'9'.repeat(32),proxy_configured:true,proxy_url_hash:hash('http://concurrent.example:8080'),proxy_display:'http://concurrent.example:8080'});
    const before=patches.length;await page.locator('#proxy-plan-apply').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-results').textContent.includes('账号代理已变更'));
    assert.equal(patches.length,before);
    // ETag 冲突不覆盖旧池，不显示宿主的含密码错误。
    await page.locator('#proxy-lines').fill('http://third.example:8080');await page.locator('#proxy-validate').click();
    await page.waitForFunction(()=>!document.querySelector('#proxy-import').disabled);conflict=true;
    await page.locator('#proxy-import').click();await page.waitForFunction(()=>document.querySelector('#proxy-status').textContent.includes('配置已被其他操作修改'));
    assert.equal(pool.proxies.length,2);assert.equal((await page.locator('#proxy-status').innerText()).includes('secret-do-not-echo'),false);
    await page.locator('#proxy-reload').click();await page.waitForFunction(()=>!document.querySelector('#proxy-validate').disabled);
    // 标签可修改但不能改变 ID；文本文件导入后可删除未引用的代理。
    await page.locator('#proxy-items .proxy-item').filter({hasText:oldProxy.id}).getByRole('button',{name:'改标签'}).click();
    await page.waitForFunction(()=>document.querySelector('#proxy-items').textContent.includes('更名代理')&&!document.querySelector('#proxy-validate').disabled);
    assert.equal(pool.proxies.find(p=>p.label==='更名代理').id,oldProxy.id);
    await page.locator('#proxy-file').setInputFiles({name:'local-proxies.txt',mimeType:'text/plain',buffer:Buffer.from('http://unused.example:8080')});
    await page.waitForFunction(()=>document.querySelector('#proxy-preview-status').textContent.includes('已载入'));
    await page.locator('#proxy-validate').click();await page.waitForFunction(()=>!document.querySelector('#proxy-import').disabled);
    await page.locator('#proxy-import').click();await page.waitForFunction(()=>document.querySelector('#proxy-count').textContent==='3 个代理'&&!document.querySelector('#proxy-validate').disabled);
    const unused=pool.proxies.find(p=>p.url==='http://unused.example:8080');assert.ok(unused);
    await page.locator('#proxy-items .proxy-item').filter({hasText:unused.id}).getByRole('button',{name:'删除',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('#proxy-count').textContent==='2 个代理'&&!document.querySelector('#proxy-validate').disabled);
    assert.equal(pool.proxies.some(p=>p.id===unused.id),false);
    // 不能删除任何读取不完整账号可能引用的代理。
    unknownAccount=true;await page.locator('#reload-accounts').click();await page.waitForFunction(()=>document.querySelector('#app').textContent.includes('代理读取失败'));
    for(const button of await page.locator('#proxy-items button').filter({hasText:'删除'}).all())assert.equal(await button.isDisabled(),true);
    unknownAccount=false;withGlobal=true;
    await reload();assert.match(await page.locator('#proxy-global').innerText(),/存在全局代理/);assert.equal((await page.locator('#proxy-global').innerText()).includes('global-secret-pass'),false);
    if(process.env.CPA_PROXY_SCREENSHOTS){
      fs.mkdirSync(process.env.CPA_PROXY_SCREENSHOTS,{recursive:true});
      await page.screenshot({path:path.join(process.env.CPA_PROXY_SCREENSHOTS,'proxy-pool-desktop.png'),fullPage:true});
    }
    await page.setViewportSize({width:375,height:812});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,'proxy UI mobile overflow');
    await page.evaluate(()=>document.documentElement.setAttribute('data-theme','dark'));
    assert.equal(await page.evaluate(()=>getComputedStyle(document.body).color),'rgb(240, 238, 232)');
    if(process.env.CPA_PROXY_SCREENSHOTS)await page.screenshot({path:path.join(process.env.CPA_PROXY_SCREENSHOTS,'proxy-pool-mobile-dark.png'),fullPage:true});
    // 首次创建目录带父 ETag，缺少账户字段 API 时不能伪报成功。
    pool=null;revision++;await reload();assert.equal(await page.locator('#proxy-count').innerText(),'0 个代理');
    await page.locator('#proxy-lines').fill('first.example:8080');await page.locator('#proxy-protocol').selectOption('http');
    await page.locator('#proxy-validate').click();await page.waitForFunction(()=>!document.querySelector('#proxy-import').disabled);await page.locator('#proxy-import').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-count').textContent==='1 个代理');assert.equal(pool.proxies.length,1);
    fieldSupport=false;const target=page.locator('.quota-card').nth(0);await target.locator('select').selectOption(pool.proxies[0].id);await target.getByRole('button',{name:'预览代理设置'}).click();await page.locator('#proxy-plan-apply').click();
    await page.waitForFunction(()=>document.querySelector('#proxy-results').textContent.includes('宿主接口不支持'));
    assert.deepEqual(errors,[]);
    console.log('PASS: proxy import/duplicates/redaction, CAS conflict, random unbound-only, partial retry fixed mapping, uncertain readback, stale binding guard, native empty-string unbind, external and unknown states, first creation, missing fields API, mobile/dark theme. PUTs='+puts);
  }finally{if(browser)await browser.close();server.closeAllConnections();await new Promise(r=>server.close(r));}
})().catch(e=>{console.error(e);process.exitCode=1;});
