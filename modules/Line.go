package modules

import (
	"database/sql"
	"errors"
)

// BadLine bad_line 表
type BadLine struct {
	RandomCityID int `json:"randomCityID" db:"randomCityID"`
}

// GetRandomCityID 随机获取一个id
func (l BadLine) GetRandomCityID(db *sql.DB) (int, error) {
	var randomCityID int
	if err := db.QueryRow("SELECT randomCityID FROM bad_line ORDER BY RANDOM() LIMIT 1").Scan(&randomCityID); err != nil {
		return 0, err
	}
	return randomCityID, nil
}

func (l BadLine) GetCityIDbyID(db *sql.DB, id int) (int, error) {
	var cityID int
	if err := db.QueryRow("SELECT randomCityID FROM bad_line WHERE randomCityID=?", id).Scan(&cityID); err != nil {
		return 0, err
	}
	return cityID, nil
}

func (l BadLine) CheckIsNotExistsAndInsert(db *sql.DB, id int) {
	_, err := l.GetCityIDbyID(db, id)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		_, _ = db.Exec("INSERT INTO bad_line (randomCityID) VALUES (?)", id)
	}
}

// GoodLine good_line 表
type GoodLine struct {
	NodeID int `json:"node_id" db:"node_id"`
}

// GetRandomCityID 随机获取一个id
func (l GoodLine) GetRandomCityID(db *sql.DB) (int, error) {
	var randomCityID int
	if err := db.QueryRow("SELECT node_id FROM good_line ORDER BY RANDOM() LIMIT 1").Scan(&randomCityID); err != nil {
		return 0, err
	}
	return randomCityID, nil
}

func (l GoodLine) GetCityIDbyID(db *sql.DB, id int) (int, error) {
	var cityID int
	if err := db.QueryRow("SELECT node_id FROM good_line WHERE node_id=?", id).Scan(&cityID); err != nil {
		return 0, err
	}
	return cityID, nil
}

func (l GoodLine) CheckIsNotExistsAndInsert(db *sql.DB, id int) {
	_, err := l.GetCityIDbyID(db, id)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		_, _ = db.Exec("INSERT INTO good_line (node_id) VALUES (?)", id)
	}
}

func (l GoodLine) DeleteById(db *sql.DB, id int) {
	_, _ = db.Exec("DELETE FROM good_line WHERE id=?", id)
}
