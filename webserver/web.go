package webserver

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"monitoring_system/database"

	_ "github.com/mattn/go-sqlite3"
)

// 全局数据库连接池
var db *sql.DB

// 去除省份名称后缀
func removeProvinceSuffix(name string) string {
	suffixes := []string{"省", "市", "自治区", "特别行政区"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return name
}

// 计算每个省份的平均值
func calculateProvinceAverages(cities []CityData) []ProvinceData {
	provinceMap := make(map[string]ProvinceData)
	for _, city := range cities {
		provinceName := removeProvinceSuffix(city.ProvinceName)
		if p, ok := provinceMap[provinceName]; ok {
			p.Cities = append(p.Cities, city)
			p.AvgSuccessRate += city.AvgSuccessRate
			p.AvgResponseTime += city.AvgResponseTime
			p.AvgDownloadRate += city.DownloadRate
			provinceMap[provinceName] = p
		} else {
			provinceMap[provinceName] = ProvinceData{
				Name:            provinceName,
				Cities:          []CityData{city},
				AvgSuccessRate:  city.AvgSuccessRate,
				AvgResponseTime: city.AvgResponseTime,
				AvgDownloadRate: city.DownloadRate,
			}
		}
	}

	var provinces []ProvinceData
	for _, p := range provinceMap {
		numCities := len(p.Cities)
		if numCities > 0 {
			p.AvgSuccessRate /= float64(numCities)
			p.AvgResponseTime /= int64(numCities)
			p.AvgDownloadRate /= float64(numCities)
		}
		provinces = append(provinces, p)
	}
	return provinces
}
func init() {
	var err error
	db, err = sql.Open("sqlite3", "./monitor.db")
	if err != nil {
		log.Fatalf("打开数据库出错: %v", err)
	}
	// 设置连接池的最大连接数等参数
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
}

// ProvinceData 省份数据结构体
type ProvinceData struct {
	Name            string
	Cities          []CityData
	AvgSuccessRate  float64
	AvgResponseTime int64
	AvgDownloadRate float64
}

// CityData 城市数据结构体
type CityData struct {
	Name            string
	AvgSuccessRate  float64
	AvgResponseTime int64
	LastUpdateTime  string
	ProvinceName    string
	DownloadRate    float64
}

// CurrentNodeInfo 用于存储当前节点信息
type CurrentNodeInfo struct {
	NodeName   string
	OutboundIP string
	TestTime   string
}

// 查询城市数据的函数，封装了日期筛选和非筛选的逻辑
func queryCities(startTimeStr, endTimeStr, sortBy string) ([]CityData, error) {
	var allCities []CityData
	var query string
	var args []interface{}
	var timeCondition string

	orderBy := ""
	if sortBy == "download_rate" {
		orderBy = "latest.download_rate DESC"
	} else if sortBy == "response_time" {
		orderBy = "latest.avg_response_time ASC"
	}

	if startTimeStr != "" && endTimeStr != "" {
		// 用户使用了筛选功能
		startTime, err := time.Parse("2006-01-02T15:04", startTimeStr)
		if err != nil {
			log.Printf("解析开始时间出错: %v", err)
			return nil, fmt.Errorf("无效的开始时间格式，请使用 YYYY-MM-DDTHH:MM 格式")
		}
		endTime, err := time.Parse("2006-01-02T15:04", endTimeStr)
		if err != nil {
			log.Printf("解析结束时间出错: %v", err)
			return nil, fmt.Errorf("无效的结束时间格式，请使用 YYYY-MM-DDTHH:MM 格式")
		}

		// 将解析后的时间转换为数据库存储的格式
		formattedStartTime := startTime.Format("2006-01-02 15:04:00")
		formattedEndTime := endTime.Format("2006-01-02 15:04:00")

		timeCondition = "n.test_time BETWEEN ? AND ?"
		args = []interface{}{formattedStartTime, formattedEndTime}
	}

	query = `
        SELECT p.name, latest.name, latest.success_rate, latest.avg_response_time, latest.test_time, latest.download_rate
        FROM provinces p
        JOIN cities c ON p.id = c.area_id
        JOIN (
            SELECT c.name, n.success_rate, n.avg_response_time, n.test_time, n.download_rate
            FROM cities c
            JOIN node_test_results n ON c.name = n.node_name
    `
	if timeCondition != "" {
		query += " WHERE " + timeCondition
	}
	query += `
            GROUP BY c.name
            HAVING n.test_time = MAX(n.test_time)
        ) latest ON c.name = latest.name
        ORDER BY p.name, ` + orderBy

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 定义东八区时区
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var provinceName string
		var cityName string
		var successRate float64
		var avgResponseTime int64
		var lastUpdateTimeStr string
		var downloadRate float64
		err := rows.Scan(&provinceName, &cityName, &successRate, &avgResponseTime, &lastUpdateTimeStr, &downloadRate)
		if err != nil {
			return nil, err
		}

		// 解析时间字符串，并使用东八区时区
		lastUpdateTime, err := time.ParseInLocation("2006-01-02 15:04:05", lastUpdateTimeStr, loc)
		if err != nil {
			return nil, err
		}

		allCities = append(allCities, CityData{
			Name:            cityName,
			AvgSuccessRate:  successRate,
			AvgResponseTime: avgResponseTime,
			LastUpdateTime:  lastUpdateTime.Format("2006-01-02 15:04:05"),
			ProvinceName:    provinceName,
			DownloadRate:    downloadRate,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return allCities, nil
}

// 处理根路径请求，展示检测数据
func showTestResults(w http.ResponseWriter, r *http.Request) {

	// 获取排序参数，默认按下载速率降序
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "download_rate"
	}

	// 获取日期筛选参数
	startTimeStr := r.URL.Query().Get("start-time")
	endTimeStr := r.URL.Query().Get("end-time")

	allCities, err := queryCities(startTimeStr, endTimeStr, sortBy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 按省份分组
	provinceMap := make(map[string][]CityData)
	for _, city := range allCities {
		provinceMap[city.ProvinceName] = append(provinceMap[city.ProvinceName], city)
	}

	var provinces []ProvinceData
	for provinceName, cities := range provinceMap {
		provinces = append(provinces, ProvinceData{
			Name:   provinceName,
			Cities: cities,
		})
	}

	// 查询最新的节点检测信息
	var currentNode CurrentNodeInfo
	err = db.QueryRow(`
        SELECT node_name, outbound_ip, test_time
        FROM node_test_results
        ORDER BY test_time DESC
        LIMIT 1
    `).Scan(&currentNode.NodeName, &currentNode.OutboundIP, &currentNode.TestTime)
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 解析 HTML 模板
	tmpl, err := template.ParseFiles("webserver/templates/results.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 构建传递给模板的数据
	data := struct {
		Provinces   []ProvinceData
		CurrentNode CurrentNodeInfo
		Sort        string
	}{
		Provinces:   provinces,
		CurrentNode: currentNode,
		Sort:        sortBy,
	}

	// 执行模板并将数据传递给模板
	err = tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// updateDownloadURL 处理更新下载 URL 的请求
func updateDownloadURL(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Path[len("/updateline/"):]
	err := database.SaveDownloadURL(db, url)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("URL updated successfully"))
}

// getLatestData 处理获取最新数据的请求
func getLatestData(w http.ResponseWriter, r *http.Request) {
	// 获取排序参数，默认按下载速率降序
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "download_rate"
	}

	// 获取日期筛选参数
	startTimeStr := r.URL.Query().Get("start-time")
	endTimeStr := r.URL.Query().Get("end-time")

	allCities, err := queryCities(startTimeStr, endTimeStr, sortBy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 计算省份平均值
	provinces := calculateProvinceAverages(allCities)

	// 查询最新的节点检测信息
	var currentNode CurrentNodeInfo
	err = db.QueryRow(`
        SELECT node_name, outbound_ip, test_time
        FROM node_test_results
        ORDER BY test_time DESC
        LIMIT 1
    `).Scan(&currentNode.NodeName, &currentNode.OutboundIP, &currentNode.TestTime)
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 构建响应数据
	data := struct {
		Provinces   []ProvinceData
		CurrentNode CurrentNodeInfo
		Sort        string
	}{
		Provinces:   provinces,
		CurrentNode: currentNode,
		Sort:        sortBy,
	}

	// 将数据转换为 JSON 格式并返回
	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// CityIDs 用于存储所有 city_id 的结构体
type CityIDs struct {
	CityID []int `json:"city_id"`
}

// BadLineEntry 用于存储 bad_line 表中的一条记录
type BadLineEntry struct {
	OutboundIP string `json:"outbound_ip"`
	CityID     int    `json:"city_id"`
}

// BadLineIPs 用于存储所有 bad_line 表记录的结构体
type BadLineIPs struct {
	Entries []BadLineEntry `json:"entries"`
}

// queryLines 通用的查询表数据的函数
func queryLines(tableName string) (interface{}, error) {
	if tableName == "good_line" {
		var cityIDs []int
		query := fmt.Sprintf("SELECT * FROM %s", tableName)
		rows, err := db.Query(query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var nullCityID sql.NullInt64
			err := rows.Scan(&nullCityID)
			if err != nil {
				return nil, err
			}
			if nullCityID.Valid {
				cityIDs = append(cityIDs, int(nullCityID.Int64))
			}
		}
		return CityIDs{CityID: cityIDs}, nil
	} else if tableName == "bad_line" {
		var entries []BadLineEntry
		query := fmt.Sprintf("SELECT * FROM %s", tableName)
		rows, err := db.Query(query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var entry BadLineEntry
			var nullCityID sql.NullInt64
			err := rows.Scan(&entry.OutboundIP, &nullCityID)
			if err != nil {
				return nil, err
			}
			if nullCityID.Valid {
				entry.CityID = int(nullCityID.Int64)
			}
			entries = append(entries, entry)
		}
		return BadLineIPs{Entries: entries}, nil
	}
	return nil, fmt.Errorf("不支持的表名: %s", tableName)
}

// handleBadLines 处理 /bad_lines 请求
func handleBadLines(w http.ResponseWriter, r *http.Request) {
	result, err := queryLines("bad_line")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := result.(BadLineIPs)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// handleGoodLines 处理 /good_lines 请求
func handleGoodLines(w http.ResponseWriter, r *http.Request) {
	result, err := queryLines("good_line")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := result.(CityIDs)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// StartWebServer 启动 Web 服务器
func StartWebServer(port int) {
	// 注册路由
	http.HandleFunc("/", showTestResults)
	http.HandleFunc("/updateline/", updateDownloadURL)
	http.HandleFunc("/latest-data", getLatestData)
	http.HandleFunc("/good_lines", handleGoodLines)
	http.HandleFunc("/bad_lines", handleBadLines)

	// 启动 Web 服务器，使用传入的端口
	address := fmt.Sprintf(":%d", port)
	log.Printf("网页服务器端口： %s", address)
	err := http.ListenAndServe(address, nil)
	if err != nil {
		log.Fatal(err)
	}
}
