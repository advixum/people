package logging

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"
	_ "github.com/joho/godotenv/autoload"
	"github.com/sirupsen/logrus"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
	"gorm.io/gorm/logger"
)

var Config = Logger(os.Getenv("LOG_MODE"))

// Logrus parameters
func Logger(env string) *logrus.Logger {
	log := logrus.New()
	log.Formatter = &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	}
	level, err := logrus.ParseLevel(env)
	if err != nil {
		log.Fatal("Failed to parse logging level:", err)
	}
	log.Level = level
	logFile := &lumberjack.Logger{
		Filename:   "logging/logs.log",
		MaxSize:    16,
		MaxBackups: 3,
		Compress:   false,
	}
	log.Out = logFile
	return log
}

// GORM-Logrus interface
func GL(logger *logrus.Logger) logger.Interface {
	return &GormLogger{
		logger: logger,
	}
}

type GormLogger struct {
	logger *logrus.Logger
}

func (l *GormLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l
}

func (l *GormLogger) Info(
	ctx context.Context,
	msg string,
	data ...interface{},
) {
	l.logger.WithContext(ctx).Infof("[GORM] "+msg, data...)
}

func (l *GormLogger) Warn(
	ctx context.Context,
	msg string,
	data ...interface{},
) {
	l.logger.WithContext(ctx).Warnf("[GORM] "+msg, data...)
}

func (l *GormLogger) Error(
	ctx context.Context,
	msg string,
	data ...interface{},
) {
	l.logger.WithContext(ctx).Errorf("[GORM] "+msg, data...)
}

func (l *GormLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	if l.logger.Level >= logrus.DebugLevel {
		elapsed := time.Since(begin)
		sql, rows := fc()
		fields := logrus.Fields{
			"rows":    rows,
			"elapsed": elapsed,
		}
		if err != nil {
			l.logger.WithFields(fields).WithError(err).Debug("[GORM] " + sql)
		} else {
			l.logger.WithFields(fields).Debug("[GORM] " + sql)
		}
	}
}

// Returns a string with the module, package, and function name with id
// that is currently executing.
func F() string {
	pc, _, _, _ := runtime.Caller(1)
	fn := runtime.FuncForPC(pc).Name()
	uuidObj := uuid.NewString()
	return fmt.Sprintf("[FUNC %v(%v)] ", fn, uuidObj[0:8])
}
