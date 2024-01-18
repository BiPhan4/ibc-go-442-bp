package logger

import (
	"log"
	"os"
	"time"
)

var (
	InfoLogger  *log.Logger
	ErrorLogger *log.Logger
)

func InitLogger() {
	path := "logs/"

	// Create directory if it doesn't exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err := os.MkdirAll(path, os.ModePerm)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Using current time to create a unique file name
	currentTime := time.Now()
	fileName := path + "ica_host_" + currentTime.Format("2006-01-02_15-04-05") + ".log"

	file, err := os.Create(fileName)
	if err != nil {
		log.Fatal(err)
	}

	InfoLogger = log.New(file, "INFO: ", log.Ldate|log.Ltime)
	ErrorLogger = log.New(file, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

// Exported function for info logging
func LogInfo(v ...interface{}) {
	InfoLogger.Println(v...)
}

// Exported function for err logging
func LogError(v ...interface{}) {
	ErrorLogger.Println(v...)
}
