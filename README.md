# gameinput

Go 游戏输入运行库（第一轮）：把键盘式虚拟输入映射为可配置动作。设备层与动作
层分离，后续可接入其他控制器；全程不读取真实设备。

## 分层

- `device/` — 虚拟键盘。调用方投喂合成消息；重复的按下/抬起消息只产生一次边沿；
  失去焦点时按确定性顺序为所有按住的键补发抬起。
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
  走统一收尾：释放旧上下文全部持续动作、关闭并复位开关、丢弃未决 tap/长按、
  清空逻辑按住集合，同时设备层开启新的输入世代（epoch），跨切换残留的物理抬起
  带旧 epoch，不能释放新一轮动作（旧抬起必须忽略，新按下照常工作）。
- **失焦 / 设备移除 / 上下文切换的统一收尾**：`Engine.Blur` /
  `Engine.DeviceRemoved` / 上下文切换共用同一收尾路径。hold 发一次
  `Deactivate`；已打开的 `switch` 发一次 `Deactivate` 并把开关复位为关；
  被打断的 tap/长按不补发 `Pulse`；不允许只清物理按键表而留下亮着的灯。
  对应设备侧调用 `VirtualKeyboard.FocusChange(false)` / `Removed()` /
  `Reset()`，其合成的强制边沿标记 `Forced`，引擎收到后只做记账，绝不重复
  释放或翻转开关。
- **失焦**：`VirtualKeyboard.FocusChange` 与 `Engine.Blur` 一起调用，保证没有
  永久按住的键，也没有残留的开关灯。
- **确定性**：引擎不读墙钟；保存配置（JSON）后用相同事件与时间戳重放，结果完全一致。

## 重放用法

```go
compiled, _ := config.Compile(cfg)
kbd := device.NewVirtualKeyboard()
eng := engine.New(compiled)
for _, ev := range recordedEvents {
    for _, out := range eng.Update(ev.TimeMs) { /* 长按到期 */ }
    if edge, ok := kbd.Push(device.Event{Key: ev.Key, Phase: ev.Phase}); ok {
        outs := eng.ApplyEdge(edge, ev.TimeMs)
        _ = outs
    }
}
// 窗口失焦时：
eng.Blur(nowMs)                                  // 动作层统一收尾
for _, edge := range kbd.FocusChange(device.FocusEvent{Focused: false}) {
    eng.ApplyEdge(edge, nowMs)                  // Forced 边沿，引擎忽略
}
// 设备移除同理：eng.DeviceRemoved(nowMs) + kbd.Removed()
// 收到 ContextSwitched 后也要 kbd.Reset()，让跨切换残留抬起进入旧 epoch。
```

`go test ./...` 覆盖重复消息、组合键、长按、开关、上下文释放、失焦、优先级及
“连按与切换交错”序列。本轮只提供库，不含改键界面。
