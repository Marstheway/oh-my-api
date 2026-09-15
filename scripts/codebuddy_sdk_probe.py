#!/usr/bin/env python3

import argparse
import json
import os
import sys
import time
import uuid
from collections import Counter
from dataclasses import asdict, dataclass
from typing import Any

import anyio
from codebuddy_agent_sdk import (
    AssistantMessage,
    CodeBuddyAgentOptions,
    CodeBuddySDKClient,
    ErrorMessage,
    ExecutionError,
    HookMatcher,
    McpServerStatus,
    PermissionResultDeny,
    ResultMessage,
    StreamEvent,
    SystemMessage,
    TextBlock,
    ThinkingBlock,
    ToolResultBlock,
    ToolUseBlock,
    UserMessage,
    authenticate,
    create_sdk_mcp_server,
    query,
    tool,
)


@dataclass
class ProbeResult:
    name: str
    status: str
    summary: str
    duration_ms: int
    details: dict[str, Any]


def now_ms() -> int:
    return time.perf_counter_ns() // 1_000_000


def usage_to_dict(usage: Any) -> dict[str, Any] | None:
    if usage is None:
        return None
    return {
        "input_tokens": getattr(usage, "input_tokens", None),
        "output_tokens": getattr(usage, "output_tokens", None),
        "cache_read_input_tokens": getattr(usage, "cache_read_input_tokens", None),
        "cache_creation_input_tokens": getattr(usage, "cache_creation_input_tokens", None),
    }


def block_text(block: Any) -> str | None:
    if isinstance(block, TextBlock):
        return block.text
    if isinstance(block, ThinkingBlock):
        return block.thinking
    return None


async def collect_query(prompt: str, options: CodeBuddyAgentOptions) -> dict[str, Any]:
    data: dict[str, Any] = {
        "messages": [],
        "init": None,
        "status_events": [],
        "assistant_texts": [],
        "assistant_thinking": [],
        "assistant_tool_uses": [],
        "user_tool_results": [],
        "stream_event_counts": {},
        "stream_text_deltas": [],
        "stream_thinking_deltas": [],
        "result": None,
        "result_meta": None,
        "error": None,
    }

    stream_counter: Counter[str] = Counter()

    try:
        async for message in query(prompt=prompt, options=options):
            data["messages"].append(type(message).__name__)

            if isinstance(message, SystemMessage):
                if message.subtype == "init":
                    init_data = message.data
                    data["init"] = {
                        "session_id": init_data.get("session_id"),
                        "model": init_data.get("model"),
                        "permission_mode": init_data.get("permissionMode"),
                        "tools": init_data.get("tools", []),
                        "mcp_servers": init_data.get("mcp_servers", []),
                    }
                elif message.subtype == "status":
                    data["status_events"].append(message.data)
                continue

            if isinstance(message, StreamEvent):
                event = message.event or {}
                event_type = event.get("type", "unknown")
                stream_counter[event_type] += 1
                if event_type == "content_block_delta":
                    delta = event.get("delta", {})
                    delta_type = delta.get("type")
                    if delta_type == "text_delta" and delta.get("text"):
                        data["stream_text_deltas"].append(delta.get("text"))
                    if delta_type == "thinking_delta" and delta.get("thinking"):
                        data["stream_thinking_deltas"].append(delta.get("thinking"))
                continue

            if isinstance(message, AssistantMessage):
                for block in message.content:
                    if isinstance(block, TextBlock):
                        data["assistant_texts"].append(block.text)
                    elif isinstance(block, ThinkingBlock):
                        data["assistant_thinking"].append(block.thinking)
                    elif isinstance(block, ToolUseBlock):
                        data["assistant_tool_uses"].append(
                            {
                                "id": block.id,
                                "name": block.name,
                                "input": block.input,
                            }
                        )
                continue

            if isinstance(message, UserMessage):
                for block in message.content:
                    if isinstance(block, ToolResultBlock):
                        text_parts: list[str] = []
                        for item in block.content:
                            if isinstance(item, dict) and item.get("type") == "text":
                                text_parts.append(item.get("text", ""))
                        data["user_tool_results"].append(
                            {
                                "tool_use_id": block.tool_use_id,
                                "is_error": block.is_error,
                                "text": "\n".join(text_parts).strip(),
                            }
                        )
                continue

            if isinstance(message, ResultMessage):
                data["result"] = message.result
                data["result_meta"] = {
                    "subtype": message.subtype,
                    "session_id": message.session_id,
                    "duration_ms": message.duration_ms,
                    "duration_api_ms": message.duration_api_ms,
                    "num_turns": message.num_turns,
                    "stop_reason": message.stop_reason,
                    "total_cost_usd": message.total_cost_usd,
                    "usage": usage_to_dict(message.usage),
                    "structured_output": message.structured_output,
                }
                continue

            if isinstance(message, ErrorMessage):
                data["error"] = {"message": str(message)}

    except ExecutionError as exc:
        data["error"] = {"type": type(exc).__name__, "message": str(exc)}
    except Exception as exc:  # pragma: no cover - probe script should keep running
        data["error"] = {"type": type(exc).__name__, "message": str(exc)}

    data["stream_event_counts"] = dict(stream_counter)
    return data


def make_result(name: str, start_ms: int, status: str, summary: str, details: dict[str, Any]) -> ProbeResult:
    return ProbeResult(
        name=name,
        status=status,
        summary=summary,
        duration_ms=now_ms() - start_ms,
        details=details,
    )


async def probe_auth(_: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    try:
        flow = await authenticate(timeout=15)
        auth_url = flow.auth_url
        result = await flow
        details = {
            "auth_url": auth_url,
            "user_id": getattr(result.userinfo, "user_id", None),
            "user_name": getattr(result.userinfo, "user_name", None),
        }
        return make_result("auth", start_ms, "pass", "SDK 认证可用，当前环境已登录", details)
    except Exception as exc:
        return make_result(
            "auth",
            start_ms,
            "fail",
            f"SDK 认证失败: {exc}",
            {"error": {"type": type(exc).__name__, "message": str(exc)}},
        )


async def probe_one_shot(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        cwd=args.cwd,
    )
    data = await collect_query("只回复 OK，不要额外内容。", options)
    result = (data.get("result") or "").strip()
    status = "pass" if result == "OK" else "fail"
    summary = f"单轮文本调用返回: {result or 'empty'}"
    return make_result("one_shot_text", start_ms, status, summary, data)


async def probe_stream(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        include_partial_messages=True,
        cwd=args.cwd,
    )
    data = await collect_query("请分三行输出：A、B、C。不要其他内容。", options)
    result = (data.get("result") or "").strip()
    saw_text_delta = bool(data.get("stream_text_deltas"))
    saw_stream_events = bool(data.get("stream_event_counts"))
    status = "pass" if result == "A\nB\nC" and saw_stream_events and saw_text_delta else "fail"
    summary = (
        f"流式调用返回 {len(data.get('stream_event_counts', {}))} 类事件，"
        f"text_delta={len(data.get('stream_text_deltas', []))}，"
        f"thinking_delta={len(data.get('stream_thinking_deltas', []))}"
    )
    return make_result("streaming", start_ms, status, summary, data)


async def probe_tool_bash(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        allowed_tools=["Bash"],
        cwd=args.cwd,
    )
    data = await collect_query("请使用 Bash 工具执行 pwd，并告诉我当前工作目录。", options)
    tool_uses = data.get("assistant_tool_uses", [])
    tool_results = data.get("user_tool_results", [])
    result_text = data.get("result") or ""
    bash_used = any(tool.get("name") == "Bash" for tool in tool_uses)
    pwd_seen = args.cwd in result_text or any(args.cwd in item.get("text", "") for item in tool_results)
    status = "pass" if bash_used and pwd_seen else "fail"
    summary = f"工具调用数={len(tool_uses)}，工具结果数={len(tool_results)}，最终答案包含 cwd={pwd_seen}"
    return make_result("tool_bash", start_ms, status, summary, data)


async def probe_multi_turn(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    secret = args.secret or str(uuid.uuid4())
    client = CodeBuddySDKClient(
        options=CodeBuddyAgentOptions(
            model=args.model,
            permission_mode="default",
            cwd=args.cwd,
        )
    )
    details: dict[str, Any] = {"secret": secret, "rounds": []}

    try:
        await client.connect()
        await client.query(f"记住暗号是 {secret}，只回复已记住。")
        round_one = await collect_client_response(client)
        details["rounds"].append(round_one)

        await client.query("刚才我让你记住的暗号是什么？只回复暗号本身。")
        round_two = await collect_client_response(client)
        details["rounds"].append(round_two)
    except Exception as exc:
        details["error"] = {"type": type(exc).__name__, "message": str(exc)}
        return make_result("multi_turn_memory", start_ms, "fail", f"多轮对话失败: {exc}", details)
    finally:
        await client.disconnect()

    remembered = (round_two.get("result") or "").strip() == secret
    status = "pass" if remembered else "fail"
    summary = f"多轮会话上下文保留={'yes' if remembered else 'no'}"
    return make_result("multi_turn_memory", start_ms, status, summary, details)


async def collect_client_response(client: CodeBuddySDKClient) -> dict[str, Any]:
    data: dict[str, Any] = {
        "messages": [],
        "assistant_texts": [],
        "assistant_thinking": [],
        "assistant_tool_uses": [],
        "assistant_models": [],
        "user_tool_results": [],
        "result": None,
        "result_meta": None,
    }

    async for message in client.receive_response():
        data["messages"].append(type(message).__name__)

        if isinstance(message, AssistantMessage):
            data["assistant_models"].append(getattr(message, "model", None))
            for block in message.content:
                if isinstance(block, TextBlock):
                    data["assistant_texts"].append(block.text)
                elif isinstance(block, ThinkingBlock):
                    data["assistant_thinking"].append(block.thinking)
                elif isinstance(block, ToolUseBlock):
                    data["assistant_tool_uses"].append(
                        {"id": block.id, "name": block.name, "input": block.input}
                    )
            continue

        if isinstance(message, UserMessage):
            for block in message.content:
                if isinstance(block, ToolResultBlock):
                    text_parts: list[str] = []
                    for item in block.content:
                        if isinstance(item, dict) and item.get("type") == "text":
                            text_parts.append(item.get("text", ""))
                    data["user_tool_results"].append(
                        {
                            "tool_use_id": block.tool_use_id,
                            "is_error": block.is_error,
                            "text": "\n".join(text_parts).strip(),
                        }
                    )
            continue

        if isinstance(message, ResultMessage):
            data["result"] = message.result
            data["result_meta"] = {
                "subtype": message.subtype,
                "session_id": message.session_id,
                "duration_ms": message.duration_ms,
                "duration_api_ms": message.duration_api_ms,
                "num_turns": message.num_turns,
                "usage": usage_to_dict(message.usage),
            }

    return data


async def probe_plan_mode(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="plan",
        allowed_tools=["Bash"],
        cwd=args.cwd,
    )
    data = await collect_query("请使用 Bash 工具执行 pwd，并告诉我当前工作目录。", options)
    bash_used = any(tool.get("name") == "Bash" for tool in data.get("assistant_tool_uses", []))
    result_text = data.get("result") or ""
    status = "pass" if data.get("init", {}).get("permission_mode") == "plan" else "fail"
    summary = (
        f"plan 模式握手值={data.get('init', {}).get('permission_mode')!r}，"
        f"实际是否仍执行 Bash={'yes' if bash_used else 'no'}"
    )
    data["observation"] = {
        "bash_used": bash_used,
        "result_contains_cwd": args.cwd in result_text,
    }
    return make_result("plan_mode_behavior", start_ms, status, summary, data)


async def probe_invalid_model(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    invalid_model = "__definitely_invalid_model__"
    options = CodeBuddyAgentOptions(
        model=invalid_model,
        permission_mode="default",
        cwd=args.cwd,
    )
    data = await collect_query("只回复 OK。", options)
    error_message = ((data.get("error") or {}).get("message") or "").strip()
    status = "pass" if error_message else "fail"
    summary = "无效模型会返回可解析错误" if error_message else "无效模型未返回预期错误"
    data["invalid_model"] = invalid_model
    return make_result("invalid_model_error", start_ms, status, summary, data)


async def probe_runtime_set_model(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    secondary_model = args.secondary_model
    client = CodeBuddySDKClient(
        options=CodeBuddyAgentOptions(
            model=args.model,
            permission_mode="default",
            cwd=args.cwd,
        )
    )
    details: dict[str, Any] = {}

    try:
        await client.connect()
        details["model_before"] = client.get_model()

        await client.query("只回复 alpha")
        first = await collect_client_response(client)

        await client.set_model(secondary_model)
        details["model_after_set"] = client.get_model()

        await client.query("只回复 beta")
        second = await collect_client_response(client)

        details["first_round"] = first
        details["second_round"] = second
    except Exception as exc:
        details["error"] = {"type": type(exc).__name__, "message": str(exc)}
        return make_result("runtime_set_model", start_ms, "fail", f"运行时切模型失败: {exc}", details)
    finally:
        await client.disconnect()

    assistant_models = second.get("assistant_models", [])
    effective_model = assistant_models[-1] if assistant_models else None
    status = "pass" if details.get("model_after_set") == secondary_model else "fail"
    summary = (
        f"SDK 本地状态已切到 {details.get('model_after_set')!r}；"
        f"第二轮 assistant.model={effective_model!r}"
    )
    details["note"] = {
        "expected_secondary_model": secondary_model,
        "effective_assistant_model": effective_model,
    }
    return make_result("runtime_set_model", start_ms, status, summary, details)


async def probe_mcp_status_api(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    client = CodeBuddySDKClient(
        options=CodeBuddyAgentOptions(
            model=args.model,
            permission_mode="default",
            cwd=args.cwd,
        )
    )
    details: dict[str, Any] = {}

    try:
        await client.connect()
        statuses = await client.mcp_server_status()
        details["statuses"] = [
            {
                "name": s.name,
                "status": s.status,
                "server_info": s.server_info,
            }
            for s in statuses
        ]
        return make_result("mcp_status_api", start_ms, "pass", "mcp_server_status 可用", details)
    except Exception as exc:
        details["error"] = {"type": type(exc).__name__, "message": str(exc)}
        return make_result(
            "mcp_status_api",
            start_ms,
            "fail",
            f"mcp_server_status 调用失败: {type(exc).__name__}",
            details,
        )
    finally:
        await client.disconnect()


async def probe_sdk_mcp_tool(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()

    @tool("echo_probe", "Echo the provided text", {"text": str})
    async def echo_probe(tool_args: dict[str, Any]) -> dict[str, Any]:
        return {"content": [{"type": "text", "text": f"echo:{tool_args.get('text', '')}"}]}

    server = create_sdk_mcp_server("probe-server", tools=[echo_probe])
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        mcp_servers={"probe": server},
        cwd=args.cwd,
    )
    data = await collect_query("调用 echo_probe 工具，参数 text=hello，然后只回复工具返回值。", options)
    mcp_connected = any(item.get("status") == "connected" for item in (data.get("init", {}).get("mcp_servers") or []))
    used_mcp_tool = any(tool_use.get("name") == "mcp__probe__echo_probe" for tool_use in data.get("assistant_tool_uses", []))
    result_text = data.get("result") or ""
    rejected = any(item.get("is_error") for item in data.get("user_tool_results", []))
    status = "pass" if mcp_connected and used_mcp_tool else "fail"
    summary = (
        f"SDK MCP server connected={mcp_connected}，mcp tool used={used_mcp_tool}，"
        f"tool_result_rejected={rejected}"
    )
    data["observation"] = {
        "result": result_text,
        "mcp_connected": mcp_connected,
        "used_mcp_tool": used_mcp_tool,
        "rejected": rejected,
    }
    return make_result("sdk_mcp_tool", start_ms, status, summary, data)


async def probe_hook_pre_tool_use(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    hook_calls: list[dict[str, Any]] = []

    async def pre_hook(input_data: dict[str, Any], tool_use_id: str | None, context: dict[str, Any]) -> dict[str, Any]:
        hook_calls.append(
            {
                "input": input_data,
                "tool_use_id": tool_use_id,
                "context": context,
            }
        )
        return {"continue_": False, "stopReason": "blocked by hook"}

    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        allowed_tools=["Bash"],
        hooks={"PreToolUse": [HookMatcher(matcher="Bash", hooks=[pre_hook])]},
        cwd=args.cwd,
    )
    data = await collect_query("请使用 Bash 执行 pwd。", options)
    hook_triggered = len(hook_calls) > 0
    blocked = any("blocked by hook" in item.get("text", "") for item in data.get("user_tool_results", []))
    status = "pass" if hook_triggered and blocked else "fail"
    summary = f"PreToolUse hook 调用次数={len(hook_calls)}，拦截结果可见={blocked}"
    data["hook_calls"] = hook_calls
    return make_result("hook_pre_tool_use", start_ms, status, summary, data)


async def probe_hook_post_tool_use(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    hook_calls: list[dict[str, Any]] = []

    async def post_hook(input_data: dict[str, Any], tool_use_id: str | None, context: dict[str, Any]) -> dict[str, Any]:
        hook_calls.append(
            {
                "input": input_data,
                "tool_use_id": tool_use_id,
                "context": context,
            }
        )
        return {"continue_": True}

    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        allowed_tools=["Bash"],
        hooks={"PostToolUse": [HookMatcher(matcher="Bash", hooks=[post_hook])]},
        cwd=args.cwd,
    )
    data = await collect_query("请使用 Bash 执行 pwd。", options)
    hook_triggered = len(hook_calls) > 0
    status = "pass" if hook_triggered else "fail"
    summary = f"PostToolUse hook 调用次数={len(hook_calls)}"
    data["hook_calls"] = hook_calls
    return make_result("hook_post_tool_use", start_ms, status, summary, data)


async def probe_tools_option_behavior(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        tools=["Read", "Bash"],
        cwd=args.cwd,
    )
    data = await collect_query("请用 Bash 执行 pwd。", options)
    bash_used = any(tool_use.get("name") == "Bash" for tool_use in data.get("assistant_tool_uses", []))
    rejected = any(item.get("is_error") for item in data.get("user_tool_results", []))
    status = "pass" if bash_used else "fail"
    summary = f"设置 tools=['Read','Bash'] 后仍发起 Bash={bash_used}，结果是否被拒绝={rejected}"
    data["observation"] = {
        "bash_used": bash_used,
        "rejected": rejected,
    }
    return make_result("tools_option_behavior", start_ms, status, summary, data)


async def probe_thinking_disabled_behavior(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        thinking={"type": "disabled"},
        include_partial_messages=True,
        cwd=args.cwd,
    )
    data = await collect_query("请分两行回复 X 和 Y，不要其他内容。", options)
    thinking_deltas = len(data.get("stream_thinking_deltas", []))
    status = "pass" if data.get("result") == "X\nY" else "fail"
    summary = f"thinking=disabled 时 thinking_delta 数={thinking_deltas}"
    data["observation"] = {
        "thinking_deltas": thinking_deltas,
        "assistant_thinking_blocks": len(data.get("assistant_thinking", [])),
    }
    return make_result("thinking_disabled_behavior", start_ms, status, summary, data)


async def probe_can_use_tool_callback(args: argparse.Namespace) -> ProbeResult:
    start_ms = now_ms()
    callback_calls: list[dict[str, Any]] = []

    async def can_use_tool(tool_name: str, tool_input: dict[str, Any], options: Any) -> PermissionResultDeny:
        callback_calls.append(
            {
                "tool_name": tool_name,
                "tool_input": tool_input,
                "options": {
                    "tool_use_id": getattr(options, "tool_use_id", None),
                    "agent_id": getattr(options, "agent_id", None),
                    "decision_reason": getattr(options, "decision_reason", None),
                },
            }
        )
        return PermissionResultDeny(message="blocked by can_use_tool")

    options = CodeBuddyAgentOptions(
        model=args.model,
        permission_mode="default",
        allowed_tools=["Bash"],
        can_use_tool=can_use_tool,
        cwd=args.cwd,
    )
    data = await collect_query("请使用 Bash 执行 pwd。", options)
    callback_used = len(callback_calls) > 0
    status = "pass" if callback_used else "fail"
    summary = f"can_use_tool 回调调用次数={len(callback_calls)}"
    data["callback_calls"] = callback_calls
    return make_result("can_use_tool_callback", start_ms, status, summary, data)


PROBES = {
    "auth": probe_auth,
    "one_shot_text": probe_one_shot,
    "streaming": probe_stream,
    "tool_bash": probe_tool_bash,
    "multi_turn_memory": probe_multi_turn,
    "plan_mode_behavior": probe_plan_mode,
    "invalid_model_error": probe_invalid_model,
    "runtime_set_model": probe_runtime_set_model,
    "mcp_status_api": probe_mcp_status_api,
    "sdk_mcp_tool": probe_sdk_mcp_tool,
    "hook_pre_tool_use": probe_hook_pre_tool_use,
    "hook_post_tool_use": probe_hook_post_tool_use,
    "tools_option_behavior": probe_tools_option_behavior,
    "thinking_disabled_behavior": probe_thinking_disabled_behavior,
    "can_use_tool_callback": probe_can_use_tool_callback,
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Probe real CodeBuddy Agent SDK runtime behavior")
    parser.add_argument("--model", default=os.environ.get("CODEBUDDY_MODEL", "gpt-5.4"))
    parser.add_argument("--secondary-model", default=os.environ.get("CODEBUDDY_SECONDARY_MODEL", "gpt-5.5"))
    parser.add_argument("--cwd", default=os.getcwd())
    parser.add_argument("--secret", default="")
    parser.add_argument(
        "--only",
        nargs="*",
        choices=sorted(PROBES.keys()),
        help="Run only selected probes",
    )
    parser.add_argument("--json-out", default="")
    parser.add_argument("--strict", action="store_true")
    return parser.parse_args()


async def main_async(args: argparse.Namespace) -> int:
    selected = args.only or list(PROBES.keys())
    results: list[ProbeResult] = []

    for name in selected:
        result = await PROBES[name](args)
        results.append(result)

    payload = {
        "probe_version": 1,
        "timestamp": int(time.time()),
        "model": args.model,
        "cwd": args.cwd,
        "results": [asdict(item) for item in results],
        "summary": {
            "pass": sum(1 for item in results if item.status == "pass"),
            "fail": sum(1 for item in results if item.status == "fail"),
        },
    }

    text = json.dumps(payload, ensure_ascii=False, indent=2)
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as fp:
            fp.write(text)
            fp.write("\n")
    print(text)

    if args.strict and any(item.status != "pass" for item in results):
        return 1
    return 0


def main() -> int:
    args = parse_args()
    try:
        return anyio.run(main_async, args)
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
