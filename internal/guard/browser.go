package guard

import (
	"errors"
	"net/url"
	"os/exec"
	"strings"
)

func browserArgs(c Config, target string) []string {
	return []string{"--user-data-dir=" + c.BrowserDir, "--proxy-server=http://" + c.Listen, "--proxy-bypass-list=localhost;127.0.0.1;[::1]", "--disable-quic", "--no-first-run", "--no-default-browser-check", target}
}
func openBrowser(c Config, target string) error {
	u, e := url.Parse(target)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("浏览器只接受完整 HTTP(S) 地址")
	}
	if _, e = ensureService(c); e != nil {
		return e
	}
	cmd := exec.Command(c.Chrome, browserArgs(c, target)...)
	// No remote debugging port, no automation extensions, no certificate bypass.
	if _, e = startDetached(cmd); e != nil {
		return errors.New("Chrome 启动失败；检查 chrome_binary 配置")
	}
	return nil
}

const browserHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ccg 浏览器诊断</title><style>
body{font:16px/1.7 system-ui,sans-serif;background:#f6f7f9;color:#182230;max-width:860px;margin:56px auto;padding:0 24px}h1{font-size:30px;margin-bottom:8px}section{background:white;border:1px solid #dde3ea;border-radius:16px;padding:20px 24px;margin:20px 0}h2{font-size:19px;margin:0 0 12px}dt{color:#566276}dd{margin:0 0 12px;overflow-wrap:anywhere}button{background:#173d68;color:white;border:0;border-radius:8px;padding:11px 18px;font:inherit;cursor:pointer}small,p{color:#566276}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:14px/1.8 ui-monospace,monospace}.tag{color:#173d68;font-weight:600}</style>
<h1>ccg 浏览器诊断</h1><p>浏览器环境与连接路径 · 本地页面 · 无统计脚本</p>
<section><h2>浏览器环境</h2><dl><dt>时区</dt><dd id="tz"></dd><dt>语言</dt><dd id="lang"></dd><dt>位置权限</dt><dd id="geo">检查中</dd><dt>浏览器版本</dt><dd id="ua"></dd></dl><small>这些是环境信息，不是封号评分。页面不会请求精确位置，也不会修改时区或设备指纹。</small></section>
<section><h2>浏览器实际请求的出口</h2><p>点击后，浏览器直接请求三个官方域名的 Cloudflare trace，不携带账号 Cookie。结果只在本页显示，不上传给 ccg 服务。</p><button id="probe">开始检查</button><pre id="result">尚未检查</pre><small>若浏览器的跨域策略或目标站点阻止读取，会显示无法检测；这不等于访问被封。结果仅代表本次 trace 请求，不能证明所有请求都使用同一 IP。</small></section>
<section><h2>连接边界</h2><p>通过 ccg browser doctor 打开时，专用 Chrome 使用指定的本地 HTTP 代理；从其他浏览器手动访问本页不能证明代理已配置。ccg 服务停止后，专用 Chrome 的外部代理连接会失败；本地 OAuth 回调仍可直连。Cookie 保存在专用配置目录中。</p><p>不安装解密证书，不开放远程调试端口。设备环境或出口变化不会改变账号的地区使用资格。</p></section>
<script>
document.getElementById('tz').textContent=Intl.DateTimeFormat().resolvedOptions().timeZone;
document.getElementById('lang').textContent=navigator.languages.join(', ');
document.getElementById('ua').textContent=navigator.userAgent;
if(navigator.permissions)navigator.permissions.query({name:'geolocation'}).then(x=>document.getElementById('geo').textContent=x.state).catch(()=>document.getElementById('geo').textContent='浏览器未提供权限状态');else document.getElementById('geo').textContent='无法读取';
document.getElementById('probe').onclick=async()=>{let b=document.getElementById('probe'),out=document.getElementById('result');b.disabled=true;out.textContent='检查中…';let hosts=['api.anthropic.com','claude.ai','platform.claude.com'];let rows=await Promise.all(hosts.map(async h=>{try{let r=await fetch('https://'+h+'/cdn-cgi/trace',{credentials:'omit',cache:'no-store',signal:AbortSignal.timeout(15000)});if(!r.ok)throw 0;let t=await r.text(),p=Object.fromEntries(t.split('\n').filter(x=>x.includes('=')).map(x=>{let i=x.indexOf('=');return [x.slice(0,i),x.slice(i+1)]}));if(!p.ip||!p.loc)throw 0;return h+'\n  IP: '+p.ip+'\n  地区: '+p.loc}catch{return h+'\n  无法读取 trace（网络或跨域限制）'}}));out.textContent=rows.join('\n\n');b.disabled=false};
</script></html>`
