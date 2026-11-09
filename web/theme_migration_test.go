package web

import (
	"os/exec"
	"strings"
	"testing"
)

func TestEarlierThemeKeySurvivesFirstPaintAndMigrates(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := `
const fs=require('fs'),vm=require('vm');
const storage=(initial)=>{const data=new Map(Object.entries(initial));return{data,getItem:k=>data.has(k)?data.get(k):null,setItem:(k,v)=>data.set(k,String(v)),removeItem:k=>data.delete(k)}};
const ctx=(localStorage)=>{const c={localStorage,document:{documentElement:{dataset:{}}},matchMedia:()=>({matches:false})};vm.createContext(c);return c};
const boot=fs.readFileSync('boot-theme.js','utf8');
const app=fs.readFileSync('app.js','utf8');
const read=app.match(/function readThemePreference\(\)\{[\s\S]*?catch\{return'light'\}\}/)[0];
const keys=app.match(/const THEME_KEY=[^;]*?,themeMedia/)[0].replace(/,themeMedia$/,';');
function check(name,ok){if(!ok)throw new Error(name)}

let s=storage({'loopforge.theme.v3':'dark'}),c=ctx(s);
vm.runInContext(boot,c);
check('first paint uses the earlier key',c.document.documentElement.dataset.theme==='dark');
check('boot script does not write',s.data.get('loopforge.theme.v3')==='dark'&&!s.data.has('agentflow.theme.v3'));
vm.runInContext(keys+read+';globalThis.out=readThemePreference()',c);
check('app migrates the earlier key',c.out==='dark'&&s.data.get('agentflow.theme.v3')==='dark'&&!s.data.has('loopforge.theme.v3'));

s=storage({'agentflow.theme.v3':'light','loopforge.theme.v3':'dark'});c=ctx(s);
vm.runInContext(boot,c);
check('current key wins at first paint',c.document.documentElement.dataset.theme==='light');
vm.runInContext(keys+read+';globalThis.out=readThemePreference()',c);
check('current key wins and the earlier key is cleared',c.out==='light'&&!s.data.has('loopforge.theme.v3'));

s=storage({'loopforge.theme.v3':'bogus'});c=ctx(s);
vm.runInContext(boot,c);
check('invalid earlier value falls back to light',c.document.documentElement.dataset.theme==='light');
vm.runInContext(keys+read+';globalThis.out=readThemePreference()',c);
check('invalid earlier value is dropped',c.out==='light'&&!s.data.has('loopforge.theme.v3')&&!s.data.has('agentflow.theme.v3'));

const login=fs.readFileSync('login.js','utf8'),bootJS=fs.readFileSync('boot.js','utf8');
check('login writes the shared key',login.includes("localStorage.setItem('agentflow.theme.v3', next)"));
check('boot.js leaves the theme to boot-theme.js',!bootJS.includes('dataset.theme'));
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestLoginPageAppliesThemeBeforeStyles(t *testing.T) {
	page := readThemeSource(t, "login.html")
	theme, styles := strings.Index(page, `<script src="/boot-theme.js`), strings.Index(page, `<link rel="stylesheet"`)
	if theme < 0 || styles < 0 {
		t.Fatal("login.html should load boot-theme.js and stylesheets")
	}
	if theme > styles {
		t.Error("login.html should apply the theme before requesting stylesheets")
	}
}
