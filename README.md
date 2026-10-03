# gameinput

Go 游戏输入运行库（第一轮）：把键盘式虚拟输入映射为可配置动作。设备层与动作
层分离，后续可接入其他控制器；全程不读取真实设备。

## 分层

- `device/` — 虚拟键盘/控制器（`NewVirtualKeyboard(id)`、`NewVirtualController(id)`）。
  调用方投喂合成消息；重复的按下/抬起消息只产生一次边沿；边沿带设备 ID；失去
  焦点时按确定性顺序为该设备所有按住的键补发抬起。
- `config/` — 可 JSON 序列化的绑定配置：组合键（顺序无关）、`hold` / `tap` /
  `long_press` / `switch` / `context_switch` 五种模式、每绑定优先级与长按阈值。
- `engine/` — 动作引擎，消费边沿并产出 `Activate` / `Deactivate` / `Pulse` /
  `ContextSwitched` 事件。

## 关键语义

- **冲突优先级**：显式 `Priority`（大者胜）→ 组合键更长者胜 → 绑定 ID 字典序。
  相同组合键可同时绑定 tap 与 long_press；不同重叠组合键按优先级贪心占用按键。
- **组合键**：全部键按住才成立；只在“补全组合键的那次按下”上登记，避免某键
  先松后剩余键被追溯触发。
- **长按**：由调用方提供单调毫秒时间戳驱动（`Update(nowMs)`），到阈值发一次
  Pulse；未到阈值就松开则不触发。
- **切换**：`switch` 每次完整按下切换开关状态；`context_switch` 切换上下文时
  释放旧上下文全部动作（含开关），清空逻辑按住集合，跨切换残留的物理抬起被忽略。
- **归属与会话**：每个按下/抬起都按「设备 ID + 会话纪元（epoch）」归属。迟到的
  旧抬起只作用于它原本的输入，不能终止后来刚启动的新动作。设备重连
  （`Engine.AttachDevice`）开启新会话，旧保持状态不会带到新设备。
- **多设备共同维持**：多个设备可以同时按住同一动作；每个设备独立参与组合键贪心
  占用，动作在「最后一个维持者松开」时才结束。
- **统一收尾**：失焦（`Engine.Blur`）与上下文切换释放所有设备；设备移除
  （`Engine.DeviceRemoved(id, now)`）只清理该设备会话——其他设备仍维持的动作保持
  激活。持续动作各发一次 `Deactivate`，已锁存开关复位为关，未决 tap /
  long_press 静默取消。
- **输入接续**：收尾时仍物理按住的键按各自设备会话进入 lingering 集合；它们的旧
  抬起事件被吞掉，不会释放新一轮（含上下文恢复后）重新登记的动作；再次按下则
  重新同步为新输入。
- **可恢复状态**：`Engine.Snapshot()` / `Restore()`（含 JSON 辅助方法）保存上下文与
  开关锁存；保存时仍按住的输入列入 `Reconfirm`，重开后一律视为未按下，必须重新
  按下确认，`PendingReconfirm()` 查询待确认清单。
- **确定性**：引擎不读墙钟；保存配置（JSON）后用相同事件与时间戳重放，结果完全一致。

## 重放用法

```go
compiled, _ := config.Compile(cfg)
kbd := device.NewVirtualKeyboard("kbd")
eng := engine.New(compiled)
for _, ev := range recordedEvents {
    for _, out := range eng.Update(ev.TimeMs) { /* 长按到期 */ }
    if edge, ok := kbd.Push(device.Event{Key: ev.Key, Phase: ev.Phase}); ok {
        outs := eng.ApplyEdge(edge, ev.TimeMs)
        _ = outs
    }
}
// 窗口失焦（释放所有设备）或拔出单个设备：
outs = append(outs, eng.Blur(nowMs)...)
outs = append(outs, eng.DeviceRemoved("pad", nowMs)...)

// 保存 / 恢复：
data, _ := eng.MarshalState()
restored := engine.New(compiled)
restored.UnmarshalState(data)
_ = restored.PendingReconfirm() // 仍需重新按下确认的输入
```

`go test ./...` 覆盖重复消息、组合键、长按、开关、上下文释放、失焦、优先级及
“连按与切换交错”序列。本轮只提供库，不含改键界面。
