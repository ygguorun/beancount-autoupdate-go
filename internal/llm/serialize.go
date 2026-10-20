package llm

import (
	"encoding/json"
	"fmt"
	"sort"

	"beancount-autoupdate/internal/beancount"
)

// SerializeTransaction 将解析结果渲染为稳定顺序的 JSON（extra 按 key 排序），
// 供 recogtest 输出与日志对比用，与请求体无关。
func SerializeTransaction(txn *beancount.TransactionData) (string, error) {
	if txn == nil {
		return "", fmt.Errorf("交易为空")
	}

	extra := make([]extraKV, 0, len(txn.Extra))
	for k, v := range txn.Extra {
		extra = append(extra, extraKV{K: k, V: v})
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].K < extra[j].K })
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return "", fmt.Errorf("序列化 extra 失败: %w", err)
	}

	out, err := json.MarshalIndent(transactionDataRaw{
		DateTime:          txn.DateTime,
		Flag:              txn.Flag,
		Payee:             txn.Payee,
		Narration:         txn.Narration,
		Tags:              txn.Tags,
		Postings:          txn.Postings,
		OrderID:           txn.OrderID,
		Extra:             extraJSON,
		SpecialDirectives: txn.SpecialDirectives,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化交易失败: %w", err)
	}
	return string(out), nil
}
