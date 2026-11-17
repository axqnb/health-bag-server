package database

import "server01.jz/cloudia-module/mcs"

// Close 关闭 MCS 管理的数据库连接，在导入 worker 退出后调用。
func Close() {
	if mcs.DB != nil {
		if db, err := mcs.DB.DB(); err == nil {
			db.Close()
		}
	}
	if mcs.SearchDB != nil {
		if db, err := mcs.SearchDB.DB(); err == nil {
			db.Close()
		}
	}
}
