package botinteraction

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"log"

	"github.com/go-sql-driver/mysql"
)

// A legacy utf8mb3 table cannot store emoji. Preserve a clear terminal reply
// rather than reconnecting with an orphaned running request and no explanation.
func (s *Service) saveReply(item domain.BotInteraction, reply, detail string) (string, string, error) {
	err := s.Store.SaveBotReply(item, reply, detail)
	if err == nil {
		return reply, detail, nil
	}
	var sqlErr *mysql.MySQLError
	if errors.As(err, &sqlErr) {
		log.Printf("wecom reply persistence failed id=%s mysql_code=%d", item.ID, sqlErr.Number)
		if sqlErr.Number == 1366 || sqlErr.Number == 3988 {
			reply = "AI 回复已生成，但数据库无法保存部分字符。请联系管理员将 Bot 消息表升级为 utf8mb4，然后重新发送请求。"
			detail = fmt.Sprintf("消息保存失败：MySQL %d，请升级 bot_interactions 字符集为 utf8mb4", sqlErr.Number)
			err = s.Store.SaveBotReply(item, reply, detail)
			return reply, detail, err
		}
	} else {
		log.Printf("wecom reply persistence failed id=%s error_type=%T", item.ID, err)
	}
	return reply, detail, err
}
