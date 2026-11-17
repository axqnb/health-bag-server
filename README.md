# AGV 任务 bag 服务端

Go + MySQL 后台服务。复用 `server01.jz/cloudia-module/mcs` 的配置、日志和 GORM/MySQL 封装；不包含 weixinnuo 的车辆同步、调度、库位等业务，不提供前端或 FTP 服务。

## 处理规则

1. 定期扫描配置的 FTP 本地接收目录，只处理普通 `.bag` 文件。
2. 连续观察文件大小和修改时间至少 `stable_seconds` 未变化。
3. 复制出临时快照，校验 ROS1 bag 记录长度、索引、消息数和消息定义，再解析数据。
4. 计算任务指标，通过一个 MySQL 事务提交六类业务数据。
5. 明确收到提交成功后，重新检查接收文件的身份、大小、修改时间和 SHA-256，再删除源 bag。
6. 全过程通过 MCS 输出日志。临时快照在本次处理结束时删除，不作为归档。

解析失败、映射缺失、业务数据冲突、MySQL 写入/提交失败，都保留源 bag，按 `retry_seconds` 重试。提交成功但删除失败，下次通过业务数据去重后继续清理。提交响应不确定时不删除源文件。

业务运行日志、常见错误和命令行说明使用中文，底层系统或数据库返回的错误原文会一并保留，便于排查。任务质量说明会转换为中文输出；数据库中的字段名、状态代码和消息格式保持不变。

不使用 SQLite，不建文件导入管理表，不保留 bag 导入历史或归档文件。MySQL 仅保存业务数据。`health_task.data_digest` 是解析后业务结果的一致性摘要，用来拒绝同任务不同内容的覆盖；不是文件记录。文件路径、文件 SHA-256、处理结果和删除结果只出现在日志中。

FTP 直接使用最终 `.bag` 文件名上传也可以；文件稳定检测只用于延后尝试，完整性最终由 bag 校验决定。不要将仍会反复改写的同名文件作为任务文件交付。失败文件不会按年龄自动删除。

## 项目结构

完整目录树、文件职责、处理链路与后续修改入口见 [项目目录与架构说明](docs/architecture.md)。

```text
main.go                       启动顺序：配置、日志、MySQL、迁移、导入服务
command/                      命令行参数、配置检查与 bag 检查
health_server.yml              配置示例
config/              配置和校验
service/bag/                 ROS1 容器校验、嵌套消息解码
model/               消息数据、六类业务模型
service/analysis/            控制误差、FFT、速度跟踪、伺服误差
server/database/            GORM 迁移和事务入库
service/ingest/              稳定检测、worker、临时快照、提交后删除
```

## 构建与快速检查

源码构建需要能获取公司私有 Go 模块，或者已经准备好模块缓存。MCS 使用与参考项目相同的 `v0.0.45`；依赖版本由 `go.mod`、`go.sum` 锁定。

```powershell
cd D:\GoLanguage\health-bag-server
go build -o bin/health-bag-server.exe .
.\bin\health-bag-server.exe -config health_server.yml -check-config
```

检查一个已经存在的真实任务 bag，将下面路径替换成实际文件路径：

```powershell
.\bin\health-bag-server.exe -config health_server.yml -check-bag D:\bags\task.bag -robot-id 1
```

`-check-bag` 只解析并打印业务结果 JSON，**不连接 MySQL、不删除 bag**。`-robot-id` 对应配置中已有的输入项；只有一个输入项时可省略。`-check-config` 只检查配置，不连接数据库。

可以先核对实际任务、消息解析及统计结果，再进行真实 MySQL 环境联调。

## MySQL 与启动

日常只需填写 `health_server.yml` 中的两部分，每项都有中文注释：

- `mysql`：`address` 是数据库 IP 和端口，`user` 是用户名，`password` 是密码，`dbname` 是数据库名。账号需要能建表、建索引及读写该业务库；MCS 初始化还会执行 `CREATE DATABASE IF NOT EXISTS`。
- `inputs`：`directory` 是 FTP 服务器实际保存 bag 的本地目录，`robot_id` 是这个目录对应的车辆 ID。目录需要提前存在，多辆车分别添加接收项，目录不能重叠。

接收目录的相对路径以配置文件所在目录为基准。地图和载荷没有可靠来源时按未知保存，不需要填写；如果确认某个接收项对应的固定值，可以在该项下按需添加 `map_name`、`is_loading`。

原来展示在 YAML 中的运行和分析参数已收回到 `config/config.go` 的默认值：每 5 秒扫描一次，文件连续稳定 60 秒后解析，失败后间隔 60 秒重试，默认串行处理任务，启动时自动初始化业务表。临时副本默认放在配置文件同级的 `data/work` 下，正常处理结束后自动清理；接收目录不能包含该临时目录。

数据库连接池、文件与消息数量限制，以及文档明确的 2 cm / 2° 控制误差阈值也使用内置默认值，无需日常配置。旧配置仍可读取，但默认 YAML 不再列出这些内部参数。

电机字段映射、扭动判定阈值、期望伺服位姿属于需要实际业务依据的可选配置。它们没有被删除或填入猜测值，按下文说明在需要时追加；未标定的扭动判定和伺服误差继续保持未知。

日志使用 `mcs.InitLog("health-bag-server")` 和 `mcs.Log`，沿用 MCS 自身的日志配置规则：读取可执行文件目录的 `logging.json`，未提供时使用默认输出。程序不额外配置日志文件路径。

初始化表后退出：

```powershell
.\bin\health-bag-server.exe -config health_server.yml -migrate-only
```

常驻运行：

```powershell
.\bin\health-bag-server.exe -config health_server.yml
```

正式联调时，将一个测试任务 bag 放入对应接收目录，等待文件稳定及扫描周期。确认日志依次出现“任务数据入库成功”和“入库后删除bag成功”，并检查 MySQL 中对应业务行。数据库故障时应出现“数据库事务提交失败”，原文件保持存在。

Linux 下直接运行可执行程序，建议配置绝对的接收和临时目录。运行账号需要拥有接收文件的读取和删除权限，以及临时目录的写权限。程序在 `main.go` 中统一处理 Ctrl+C 和 SIGTERM，停止扫描、等待处理线程退出并关闭数据库连接。二进制运行不依赖 Go 工具链、ROS Master、ROS Kinetic 或 Python。

## 数据库

只创建以下六张 `health_` 业务表，不修改调度项目的 `mission` 等表：

- `health_task`：车辆、地图、载荷、任务起终点、起止时间、质量说明及计算版本。公共字段通过子表 `task_id` 关联。
- `health_reach_precision`：每条到点事件的三类 x/y/yaw 原值、到点类型和原 JSON。
- `health_process_control`：任务级横向、角度超限样本数，绝对误差均值/最大值/P95，以及定位跳动计数。
- `health_twisting`：角速度统计、频谱峰值和可选扭动事件统计。
- `health_actuator_tracking`：每任务、每配置通道的指令/反馈/误差统计及响应时间。
- `health_servo_result`：Action 结果状态、goal_id、tag_id、vs_type、耗时、原始位姿/JSON，以及可选的对接误差。

模型和实际列定义见 `model/tables.go`。`start_timestamp`、`end_timestamp` 使用 `DECIMAL(20,9)`，bag 消息时间使用纳秒整数。程序不能提高源头 `float64` 时间戳本身的精度。

任务唯一键由车辆 ID、起终点、起止时间构成。相同任务、相同业务数据重试不会重复插入指标；同任务不同数据报冲突并保留 bag，不静默覆盖。指标配置、车辆上下文或计算版本变化也可能产生冲突，应明确处理已有业务数据后再重新计算。

## 解析与统计口径

支持以下实际话题：

```text
/nav_manager/mission_info
/nav_manager/nav_reach_precision
/nav_manager/process_control_error
/emma_odom
/mux_vel
/jz_cmd_feedback
/ibvs_constrained_action/result
```

前三个话题先按 bag 自带 ROS 定义解码 `jzstd_msgs/DataCollection`，再解码 `data` JSON。其余按 bag 自带的嵌套定义解析。支持无压缩、LZ4、BZ2 的读取路径。单条容器记录及解压后 chunk 上限为 64 MiB，单个消息数组最多 100 万个元素；超过限制会保留源文件并报错。

一个 bag 必须包含一个有效任务摘要；相同摘要的重复消息可以去重，多个不同任务摘要、摘要缺失、未知话题、字段缺失或消息版本不支持都会拒绝入库。不使用文件名猜测任务信息。任务结束摘要的入包时间允许晚于 `end_timestamp`；过程样本按 bag 记录时间检查必须位于任务范围内。

### 过程控制

- 横向阈值默认 0.02 m；角度阈值默认 2°，计算时换成弧度。
- `*_exceed_count` 表示超限采样点数，不是独立故障事件次数。
- 误差、速度统计采用绝对值；P95 使用排序后线性插值。
- `loc_jump_times` 按累计计数增量处理；计数回退视作复位分段，避免把每条累计值相加。
- 没有有效样本时，状态为 `missing`，数值指标为 NULL；计数字段必须结合状态解释。

### 到点与伺服

`Closedloop_error` 大小写按现有发布格式识别。当前车端到点字段来自自学习配置/补偿量，服务端保存来源原值并在质量说明中标注，不把它自动解释成本次实测残差。未收到到点消息可能是未开启对应功能、采集开关未开启或该任务没有发布；不能当作误差为零。

伺服原始 `baseToTarget` 及耗时始终保存。只有按需追加了 `analysis.servo_expected_pose`，且四元数有效时，才计算 `T_expected^-1 * T_measured` 的平面 x/y/z/yaw 误差。默认不配置，派生误差为 NULL，表示未知，不将相对目标位姿直接当成误差。当前 bag 没有采到的导航后伺服结果，服务器无法补回。

### 扭动

使用 `/emma_odom.twist.twist.angular.z`，按实际消息时间戳去重、分段和重采样；跨越明显时间缺口不插值。每窗去均值和线性趋势，使用 Hann 窗及 FFT，并校正窗口增益。`dominant_freq` 为 Hz，`main_freq_dominant` 为 rad/s 幅值，不是 PSD 功率。

任务结果取有效窗口中幅值最大的频谱峰值。默认仅计算频谱特征，`has_twisting`、事件数和时长为 NULL。需要判定扭动时，按需追加 `analysis.twisting.amplitude_threshold_rad_s`，填写经过现场标定的幅值阈值；程序会合并连续超限窗口并按最短时长统计事件。事件边界是窗口级估计，正常转弯仍需结合实际场景标定。短于窗口或采样不足时标记 `insufficient`。

### 执行器映射

协议未确认时，程序不会猜测 `data[0]` 的含义。默认配置没有电机映射；bag 里存在 `/mux_vel` 或 `/jz_cmd_feedback` 时，需要按协议追加 `analysis.tracking.channels`，否则保留整个 bag 并用中文提示，填写映射后自动重试。

下面只展示配置写法，**不是你车型的默认协议**。只有确认双方 `data[0]` 都代表 m/s 时才适用；如原始单位为 mm/s，使用 `scale: 0.001`。不同类型、索引、齿轮比和轮径换算必须按实际协议填写。

```yaml
analysis:
  tracking:
    channels:
      - name: base_linear
        quantity: linear
        error_threshold: 0.05
        step_threshold: 0.1
        reach_fraction: 0.9
        command:
          - actuator_index: 0
            motor_index: 0
            data_index: 0
            scale: 1
        feedback:
          - actuator_index: 0
            motor_index: 0
            data_index: 0
            scale: 1
```

`command` 和 `feedback` 均支持多个字段加权求和，用于确认后的轮速到整车速度换算。可选 `actuator_type`、`motor_type` 用于核对消息类型。`quantity` 支持 `linear`、`angular`、`motor`；对应统计按通道存储，线速度通道和角速度通道是不同记录。

默认使用 bag 接收时间配对最新的历史指令，超过 `max_pair_gap_seconds` 计为未匹配。只有确认双方 `stamp` 单位为秒且处于同一时钟域，才开启 `use_message_stamp`。

响应时间定义为有效指令阶跃发生后，反馈达到变化量 `reach_fraction` 的首次耗时，最终记录有效阶跃的平均值；没有有效阶跃或未达到阈值时为 NULL，不用两个采样点的时间差冒充响应延迟。

## 实际环境联调

先通过 `-check-bag` 检查真实车端数据，再配置 MySQL 与 FTP 接收目录启动常驻服务。核对六类业务表的内容，并确认解析或入库失败时源 bag 保留、事务提交成功后源 bag 删除。

入库成功后源 bag 按要求删除，因此原始高频采样不会再作为独立文件留存。数据库统计不能恢复全部原始时序，也不能在日后无损重算任意新指标。


