# gameinput

`gameinput` 是一个不读取真实设备的确定性游戏输入运行库。设备层只产生 `InputEvent`，动作层由 `Engine` 将事件映射为 `ActionEvent`，后续控制器只需复用相同的按钮事件模型。

## 绑定类型

- `tap`：按键组合满足并发生按下边沿时触发一次按下/释放。
- `hold`：全部按键保持达到 `long_press_ms` 后按下，任一键释放或失焦时释放；提前释放不会触发。
- `toggle`：每次真实按下边沿翻转动作开关；上下文切换或失焦会关闭并释放。
- `context`：触发一次动作后切换到 `target_context`。

组合键写成多个 `keys`。同一按下边沿有多个绑定时，依次按以下规则选择：

1. `priority` 数值更大。
2. 组合键数量更多。
3. 绑定 `id` 字典序更小。

## 状态与重放

事件必须携带单调递增的 `seq` 和不回退的 `timestamp_ms`。完全相同的重复事件会被忽略；相同序号但内容不同会报错。相同配置和相同事件序列在任何时候重放都会得到相同结果。

上下文切换会释放旧上下文的持续动作和开关动作，并取消未到期长按。物理按键状态会保留，避免控制器之后补发释放事件时无法匹配；动作状态不会泄漏到新上下文。`focus_lost` 会释放所有持续动作、开关动作和未到期长按，并清空按键状态。

配置通过 `LoadConfig` 和 `SaveConfig` 读写 JSON；保存前会规范化并校验，绑定排序后输出以保持确定性。

## 示例

```go
cfg := input.Config{
    Bindings: []input.Binding{
        {ID: "jump", Action: "jump", Keys: []input.Button{"Space"}},
        {ID: "run", Action: "run", Keys: []input.Button{"Shift"}, Trigger: input.TriggerHold, LongPressMS: 300},
    },
}

engine, err := input.NewEngine(cfg)
if err != nil {
    panic(err)
}

keyboard := input.NewVirtualKeyboard("player-1")
result, err := engine.Replay([]input.InputEvent{
    keyboard.Down(1, 0, "Shift"),
    keyboard.Down(1, 0, "Shift"),   // 硬件自动重复会被忽略
    keyboard.Up(3, 400, "Shift"),
})
```
