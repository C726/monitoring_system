package database

import (
	"database/sql"
	"fmt"
	"github.com/sirupsen/logrus"
	"log"
	"monitoring_system/http_requests"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// InitDatabase 初始化数据库
func InitDatabase() *sql.DB {
	db, err := OpenDatabase()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"Error": err,
		}).Fatal("打开数据库出错")
	}
	// 设置最大打开连接数
	db.SetMaxOpenConns(10)
	// 设置最大空闲连接数
	db.SetMaxIdleConns(5)

	// 创建表
	if err = CreateTables(db); err != nil {
		logrus.WithFields(logrus.Fields{
			"Error": err,
		}).Fatal("创建表出错")
	}
	return db
}

// OpenDatabase 打开 SQLite 数据库
func OpenDatabase() (*sql.DB, error) {
	return sql.Open("sqlite3", "./monitor.db")
}

// ResetGoodCount 将 cities 表中指定 id 的 good_count 置为 0
func ResetGoodCount(db *sql.DB, randomCityID int) error {
	return UpdateGoodCount(db, randomCityID, false)
}

// CreateTables 创建省份表、城市表、节点检测结果表、下载 URL 表、good_line 表和 bad_line 表
func CreateTables(db *sql.DB) error {
	// 创建省份表
	_, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS provinces (
            id INTEGER PRIMARY KEY,
            name TEXT
        );
    `)
	if err != nil {
		return err
	}

	// 创建城市表，添加 good_count 和 bad_count 字段
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS cities (
            id INTEGER PRIMARY KEY,
            name TEXT,
            line_type TEXT,
            max INTEGER,
            area_id INTEGER,
            good_count INTEGER DEFAULT 0,
            bad_count INTEGER DEFAULT 0,
            FOREIGN KEY (area_id) REFERENCES provinces(id)
        );
    `)
	if err != nil {
		return err
	}

	// 创建 node_test_results 表，添加 outbound_ip 列、download_rate 列和 node_id 列
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS node_test_results (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            node_name TEXT NOT NULL,
            success_rate REAL,
            avg_response_time INTEGER,
            test_time TEXT,
            outbound_ip TEXT,  -- 添加 outbound_ip 列
            download_rate REAL,  -- 添加 download_rate 列，用于存储下载速率
            node_id INTEGER  -- 新增 node_id 列
        );
    `)
	if err != nil {
		return err
	}

	// 创建下载 URL 表
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS download_url (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            url TEXT
        );
    `)
	if err != nil {
		return err
	}

	// 创建 good_line 表
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS good_line (
            node_id INTEGER PRIMARY KEY
        );
    `)
	if err != nil {
		return err
	}

	// 创建 bad_line 表，将主键改为 TEXT 类型以存储 IP 地址，并添加 randomCityID 字段
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS bad_line (
            outbound_ip TEXT PRIMARY KEY,
            randomCityID INT
        );
    `)
	if err != nil {
		return err
	}

	// 创建 bad_ips 表，包含 outboundIP 和 randomCityID 字段
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS bad_ips (
            outboundIP TEXT,
            randomCityID INTEGER,
            PRIMARY KEY (outboundIP, randomCityID)
        );
    `)
	if err != nil {
		return err
	}

	return nil
}

// SaveProvinces 存储省份列表到数据库
func SaveProvinces(db *sql.DB, provinces []http_requests.Province) error {
	for _, province := range provinces {
		_, err := db.Exec("INSERT OR IGNORE INTO provinces (id, name) VALUES (?,?)", province.ID, province.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

// SaveNodes 存储城市列表到数据库
func SaveNodes(db *sql.DB, nodes []http_requests.Node) error {
	for _, node := range nodes {
		_, err := db.Exec("INSERT OR IGNORE INTO cities (id, name, line_type, max, area_id) VALUES (?,?,?,?,?)", node.ID, node.Name, node.LineType, node.Max, node.AreaID)
		if err != nil {
			return err
		}
	}
	return nil
}

// PrintAssociations 打印省份和城市的关联关系
func PrintAssociations(db *sql.DB) {
	rows, err := db.Query(`
        SELECT provinces.id, provinces.name, cities.id, cities.name
        FROM provinces
        JOIN cities ON provinces.id = cities.area_id
        ORDER BY provinces.id, cities.id
    `)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	var currentProvinceID int
	var firstProvince = true

	for rows.Next() {
		var provinceID int
		var provinceName string
		var cityID int
		var cityName string
		err := rows.Scan(&provinceID, &provinceName, &cityID, &cityName)
		if err != nil {
			log.Fatal(err)
		}

		if firstProvince || provinceID != currentProvinceID {
			if !firstProvince {
				fmt.Println()
			}
			//fmt.Printf("Province ID: %d, Province Name: %s\n", provinceID, provinceName)
			currentProvinceID = provinceID
			firstProvince = false
		}
		//fmt.Printf("  City ID: %d, City Name: %s\n", cityID, cityName)
	}

	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
}

// GetAllCityIDs 获取所有城市 ID
func GetAllCityIDs(db *sql.DB) ([]int, error) {
	rows, err := db.Query("SELECT id FROM cities")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cityIDs []int
	for rows.Next() {
		var cityID int
		err := rows.Scan(&cityID)
		if err != nil {
			return nil, err
		}
		cityIDs = append(cityIDs, cityID)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cityIDs, nil
}

// CheckDataExists 检查数据库中是否已经存在省份和城市信息
func CheckDataExists(db *sql.DB) (bool, error) {
	var provinceCount int
	err := db.QueryRow("SELECT COUNT(*) FROM provinces").Scan(&provinceCount)
	if err != nil {
		return false, err
	}
	if provinceCount == 0 {
		return false, nil
	}

	var cityCount int
	err = db.QueryRow("SELECT COUNT(*) FROM cities").Scan(&cityCount)
	if err != nil {
		return false, err
	}
	return cityCount > 0, nil
}

// SaveNodeTestResult 保存节点检测结果到数据库
func SaveNodeTestResult(db *sql.DB, nodeName string, successRate float64, avgResponseTime int64, downloadRate float64, nodeID int) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	_, err := db.Exec(`
        INSERT INTO node_test_results (node_name, success_rate, avg_response_time, test_time, outbound_ip, download_rate, node_id)
        VALUES (?,?,?,?,?,?,?)
    `, nodeName, successRate, avgResponseTime, now, "", downloadRate, nodeID)
	if err != nil {
		log.Printf("保存节点 %s 检测结果到数据库时出错: %v", nodeName, err)
		return err
	}
	return nil
}

// SaveDownloadURL 保存下载 URL 到数据库
func SaveDownloadURL(db *sql.DB, url string) error {
	// 先删除表中的所有记录
	_, err := db.Exec("DELETE FROM download_url")
	if err != nil {
		return err
	}

	// 插入新的 URL
	_, err = db.Exec("INSERT INTO download_url (url) VALUES (?)", url)
	if err != nil {
		return err
	}

	return nil
}

// 查询 randomCityID 是否存在 bad_line 表中
func CheckNodeIDExistsInBadLine_id(db *sql.DB, randomCityID int) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM bad_line WHERE randomCityID =?", randomCityID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetDownloadURL 从数据库中获取下载 URL
func GetDownloadURL(db *sql.DB) (string, error) {
	var url string
	err := db.QueryRow("SELECT url FROM download_url").Scan(&url)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return url, nil
}

// InsertIntoGoodLine 插入 node_id 到 good_line 表
func InsertIntoGoodLine(db *sql.DB, nodeID int) error {
	_, err := db.Exec("INSERT OR IGNORE INTO good_line (node_id) VALUES (?)", nodeID)
	if err != nil {
		return err
	}
	return nil
}

// DeleteFromGoodLine 从 good_line 表中删除 node_id
func DeleteFromGoodLine(db *sql.DB, nodeID int) error {
	_, err := db.Exec("DELETE FROM good_line WHERE node_id = ?", nodeID)
	if err != nil {
		return err
	}
	return nil
}

// InsertIntoBadLine 插入 outbound_ip 到 bad_line 表，并传入 randomCityID
func InsertIntoBadLine(db *sql.DB, outboundIP string, randomCityID int) error {
	_, err := db.Exec("INSERT OR IGNORE INTO bad_line (outbound_ip, randomCityID) VALUES (?,?)", outboundIP, randomCityID)
	if err != nil {
		return err
	}
	return nil
}

// DeleteFromBadLine 从 bad_line 表中删除 outbound_ip
func DeleteFromBadLine(db *sql.DB, outboundIP string) error {
	_, err := db.Exec("DELETE FROM bad_line WHERE outbound_ip = ?", outboundIP)
	if err != nil {
		return err
	}
	return nil
}

// DeleteFromBadLine_id 从 bad_line 表中删除指定 randomCityID 的记录
func DeleteFromBadLine_id(db *sql.DB, randomCityID int) error {
	_, err := db.Exec("DELETE FROM bad_line WHERE randomCityID = ?", randomCityID)
	if err != nil {
		return err
	}
	return nil
}

// CheckNodeIDExistsInBadLine 检查 outbound_ip 是否存在于 bad_line 表
func CheckNodeIDExistsInBadLine(db *sql.DB, outboundIP string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM bad_line WHERE outbound_ip = ?", outboundIP).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateGoodCount 更新城市的 good_count
func UpdateGoodCount(db *sql.DB, randomCityID int, increment bool) error {
	var query string
	if increment {
		query = "UPDATE cities SET good_count = good_count + 1 WHERE id = ?"
	} else {
		query = "UPDATE cities SET good_count = 0 WHERE id = ?"
	}
	_, err := db.Exec(query, randomCityID)
	if err != nil {
		return err
	}
	return nil
}

// UpdateBadCount 更新城市的 bad_count
func UpdateBadCount(db *sql.DB, randomCityID int, increment bool) error {
	var query string
	if increment {
		query = "UPDATE cities SET bad_count = bad_count + 1 WHERE id = ?"
	} else {
		query = "UPDATE cities SET bad_count = 0 WHERE id = ?"
	}
	_, err := db.Exec(query, randomCityID)
	if err != nil {
		return err
	}
	return nil
}

// GetGoodCount 获取城市的 good_count
func GetGoodCount(db *sql.DB, randomCityID int) (int, error) {
	var count int
	err := db.QueryRow("SELECT good_count FROM cities WHERE id = ?", randomCityID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetBadCount 获取城市的 bad_count
func GetBadCount(db *sql.DB, randomCityID int) (int, error) {
	var count int
	err := db.QueryRow("SELECT bad_count FROM cities WHERE id = ?", randomCityID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// InsertIntoBadIPs 插入 outboundIP 和 randomCityID 到 bad_ips 表
func InsertIntoBadIPs(db *sql.DB, outboundIP string, randomCityID int) error {
	_, err := db.Exec("INSERT OR IGNORE INTO bad_ips (outboundIP, randomCityID) VALUES (?,?)", outboundIP, randomCityID)
	if err != nil {
		return err
	}
	return nil
}
