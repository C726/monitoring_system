# monitoring_system
## 1. 项目介绍
本项目是一个监控系统，主要用于监控服务器的状态，包括CPU、内存、磁盘、网络等。
## 2. 项目结构
```
├── README.md
├── config
│   └── config.yaml
├── main.go
├── monitor
│   ├── cpu.go
│   ├── disk.go
│   ├── memory.go
│   ├── network.go
│   └── system.go
└── utils
    └── utils.go
```
## 3. 项目运行
```
go run main.go
```
## 4. 项目配置
```
server:
  port: 8080
``` # monitoring_system
