package installer

import (
	"fmt"
	"log"
	"time"

	"imagecaster-server/dl"
	"imagecaster-server/process"
)

func (inst *Installer) progressReporter(key string, lb *process.RingBuffer) func(done, total int64) {
	var lastLog, lastTime int64
	return func(done, total int64) {
		now := time.Now().UnixMilli()
		if done-lastLog < 10*1024*1024 && now-lastTime < 5000 {
			return
		}
		lastLog = done
		lastTime = now
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf(" (%.0f%%)", float64(done)/float64(total)*100)
		}
		msg := fmt.Sprintf("Downloading %s%s", dl.FormatBytes(done), pct)
		inst.setProgress(key, msg)
		lb.Write(msg)
		log.Printf("[%s] %s", key, msg)
	}
}
