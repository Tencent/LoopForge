import React from "react";
import {render,screen,fireEvent,waitFor} from "@testing-library/react";
import {beforeEach,expect,test,vi} from "vitest";
const {apiMock,writeMock}=vi.hoisted(()=>({apiMock:vi.fn(),writeMock:vi.fn()}));
vi.mock("../src/api",()=>({api:apiMock,write:writeMock,sessionPath:id=>`/api/assistant/sessions/${id}`}));
import NativeApprovalPanel from "../src/NativeApprovalPanel";
const chat={session:{id:"chat1"},user:{id:1},busy:true};
beforeEach(()=>{apiMock.mockReset();writeMock.mockReset();writeMock.mockResolvedValue({});});
test("shows the exact actor's action and sends a single-action decision",async()=>{
 apiMock.mockResolvedValue([{id:"a1",actorId:1,tool:"Bash",input:{command:"printf approved"}},{id:"a2",actorId:2,tool:"Write",input:{path:"other.txt"}}]);
 render(<NativeApprovalPanel chat={chat}/>);
 expect(await screen.findByText("确认执行 Bash")).toBeInTheDocument();
 expect(screen.getByText(/printf approved/)).toBeInTheDocument();
 expect(screen.queryByText("确认执行 Write")).not.toBeInTheDocument();
 fireEvent.click(screen.getByRole("button",{name:"批准本次操作"}));
 await waitFor(()=>expect(writeMock).toHaveBeenCalledWith("/api/assistant/sessions/chat1/native-approvals/a1",{decision:"allow"}));
 await waitFor(()=>expect(screen.queryByText("确认执行 Bash")).not.toBeInTheDocument());
});
test("clears pending actions as soon as the turn ends",async()=>{
 apiMock.mockResolvedValue([{id:"a1",actorId:1,tool:"Write",input:{path:"file.txt"}}]);
 const {rerender}=render(<NativeApprovalPanel chat={chat}/>);
 await screen.findByText("确认执行 Write");
 rerender(<NativeApprovalPanel chat={{...chat,busy:false}}/>);
 await waitFor(()=>expect(screen.queryByText("确认执行 Write")).not.toBeInTheDocument());
});
