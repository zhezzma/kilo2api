package check

import (
	"kilo2api/common/config"
	logger "kilo2api/common/loggger"
)

func CheckEnvVariable() {
	logger.SysLog("environment variable checking...")

	//if config.MysqlDsn == "" {
	//	logger.FatalLog("环境变量 MYSQL_DSN 未设置")
	//}

	if config.BackendApiEnable == 1 && config.BackendSecret == "" {
		//logger.FatalLog("环境变量 BACKEND_SECRET 未设置")
	}

	logger.SysLog("environment variable check passed.")
}
