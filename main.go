package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"monitoring_system/checker"
	"monitoring_system/database"
	"monitoring_system/http_requests"
	"monitoring_system/tcp"
	"monitoring_system/webserver"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// 最大并发数
const maxConcurrency = 5

// 定义一个互斥锁
var dbMutex sync.Mutex

// 定义一个全局的 map 来存储 curl 出现 exit 28 和 18 的 randomCityID 和对应的 outboundIP
var curlExitErrorMap = make(map[int]map[string]struct{})
var curlExitErrorMutex sync.Mutex

func main() {
	// 打开 SQLite 数据库
	db := database.InitDatabase()
	defer func(db *sql.DB) {
		err := db.Close()
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"Error": err,
			}).Fatal("数据库关闭失败")
		}
	}(db)

	// 读取配置文件
	config, err := http_requests.ReadConfig()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"Error": err,
		}).Fatal("读取配置文件出错")
	}

	connectOut := config.ConnectOut
	connectBase := config.ConnectBaseURL

	targetAddr := config.ConnectBaseURL
	if connectOut == "true" {
		// 启用 connect_out 模块
		logrus.Warn("【TCP_SERVER_MOD】采用外部模式")
		// 移除协议前缀
		targetAddr = strings.TrimPrefix(connectBase, "http://")

	} else {
		// 启用 tcp 模块
		logrus.Warn("【TCP_SERVER_MOD】采用内部模式")
		targetAddr, err = tcp.ListenTCP()
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"Error": err,
			}).Fatal("启动 TCP 监听出错")
		}
	}

	// 检查数据库中是否已经存在省份和城市信息
	dataExists, err := database.CheckDataExists(db)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"Error": err,
		}).Fatal("检查数据库数据时出错")
	}

	if dataExists {
		// 数据库有数据，询问用户是否更新
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("数据库中已经存在省份和城市信息，是否需要更新城市数据库？(y/n): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"Error": err,
			}).Fatal("读取用户输入时出错")
		}
		input = strings.TrimSpace(strings.ToLower(input))

		if input == "y" {
			// 用户选择更新，重新拉取省份和城市数据并写入
			updateDatabase(db, config)
		}
	} else {
		// 数据库没有数据，直接初始化查询并写入
		updateDatabase(db, config)
	}

	// 定义检测间隔时间，修改为 3 秒
	interval := 3 * time.Second

	// 启动 Web 服务器
	go webserver.StartWebServer(config.WebServerPort)

	// 信号量通道
	sem := make(chan struct{}, maxConcurrency)

	for _, tradeID := range config.TradeIDs {
		go func(tID int) {
			for {
				performChecks(db, tID, config, sem, targetAddr)
				time.Sleep(interval)
			}
		}(tradeID)
	}
	// 创建检查器实例
	mapChecker := checker.NewChecker(db, config, curlExitErrorMap, &curlExitErrorMutex)

	// 启动检查器协程
	go func() {
		for {
			logrus.Warn("\n【Checker】启动成功...")
			mapChecker.Check()
			time.Sleep(5 * time.Second) // 每 5 秒检查一次
		}
	}()
	// 防止 main 函数退出
	select {}
}
