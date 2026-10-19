package web

import (
	"os/exec"
	"testing"
)

func TestAssistantToolFailureKeepsRealTargetAndSurvivesBulkSettle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const source=fs.readFileSync('assistant.js','utf8');
class Element{
 constructor(tag){this.tag=tag;this.children=[];this.dataset={};this.classes=new Set();this.classList={add:(...names)=>names.forEach(x=>this.classes.add(x)),remove:(...names)=>names.forEach(x=>this.classes.delete(x)),contains:name=>this.classes.has(name)}}
 get className(){return [...this.classes].join(' ')} set className(value){this.classes=new Set(value.split(/\s+/).filter(Boolean))}
 set innerHTML(value){this.children=[];for(const name of ['ai-tool-spinner','ai-tool-line-text']){const span=new Element('span');span.className=name;this.append(span)}}
 append(...items){this.children.push(...items)}
 querySelectorAll(selector){const matches=[];for(const child of this.children){if(child.classList?.contains(selector.slice(1)))matches.push(child);matches.push(...(child.querySelectorAll?.(selector)||[]))}return matches}
 querySelector(selector){return this.querySelectorAll(selector)[0]||null}
}
const document={createElement:tag=>new Element(tag)},collapseThinking=()=>{},timelineScroll=()=>{};let timeline=null;
eval(source.slice(source.indexOf('  function settleToolLine('),source.indexOf('  function newThinkingSeg(')));
eval(source.slice(source.indexOf('  function newToolLine('),source.indexOf('  function setStatusLine(')));
eval(source.slice(source.indexOf('  function pushThought('),source.indexOf('  async function consumeAssistantStream(')));
const pending=new Element('div');
pushThought(pending,{kind:'tool',id:'retry',tool:'agentflow__runs_retry',text:'正在调用 agentflow__runs_retry'});
pushThought(pending,{kind:'toolDone',id:'retry',failed:true});
const retry=pending.children[0];
assert.equal(retry.querySelector('.ai-tool-line-text').textContent,'正在调用 agentflow__runs_retry','retain actual tool target');
assert.ok(retry.classList.contains('is-failed'));assert.ok(retry.querySelector('.ai-tool-error'));assert.equal(retry.querySelector('.ai-tool-state').textContent,'失败');assert.equal(retry.querySelector('.ai-tool-check'),null);
for(const id of ['read','list'])pushThought(pending,{kind:'tool',id,tool:'read',text:'正在调用 read'});
pushThought(pending,{kind:'toolDone',id:'read'});
pending.querySelectorAll('.ai-tool-line').forEach(settleToolLine);
assert.ok(retry.classList.contains('is-failed'));assert.ok(retry.querySelector('.ai-tool-error'));
for(const line of pending.children.slice(1)){assert.equal(line.classList.contains('is-failed'),false,'forEach index must not become failure');assert.ok(line.querySelector('.ai-tool-check'))}
// An error arriving after an optimistic/final settle must replace its success mark.
pushThought(pending,{kind:'toolDone',id:'read',failed:true});assert.ok(pending.children[1].querySelector('.ai-tool-error'));assert.equal(pending.children[1].querySelector('.ai-tool-check'),null);
// Hidden terminal submissions remain quiet on success, but their failure is visible.
pushThought(pending,{kind:'toolDone',id:'submit-success'});assert.equal(pending.children.length,3);
pushThought(pending,{kind:'toolDone',id:'submit-failure',failed:true});
const hidden=pending.children[3];assert.equal(hidden.querySelector('.ai-tool-line-text').textContent,'草稿 / 工具调用失败');assert.ok(hidden.querySelector('.ai-tool-error'));
pushThought(pending,{kind:'toolDone',id:'submit-failure',failed:true});assert.equal(pending.children.length,4,'duplicate failed completion stays one row');
pending.querySelectorAll('.ai-tool-line').forEach(settleToolLine);assert.ok(hidden.querySelector('.ai-tool-error'));
const css=fs.readFileSync('assistant.css','utf8');assert.match(css,/\.ai-tool-line\.is-failed\{color:var\(--red\)\}/);assert.match(css,/\.ai-tool-error\{/);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("assistant tool status regression: %v\n%s", err, out)
	}
}
