package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mayswind/ezbookkeeping/pkg/core"
	"github.com/mayswind/ezbookkeeping/pkg/datastore"
	"github.com/mayswind/ezbookkeeping/pkg/errs"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai/alpaca"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai/statementparser"
	"github.com/mayswind/ezbookkeeping/pkg/llm"
	"github.com/mayswind/ezbookkeeping/pkg/llm/data"
	"github.com/mayswind/ezbookkeeping/pkg/log"
	"github.com/mayswind/ezbookkeeping/pkg/models"
	"github.com/mayswind/ezbookkeeping/pkg/services"
	"github.com/mayswind/ezbookkeeping/pkg/settings"
	"github.com/mayswind/ezbookkeeping/pkg/utils"
	"xorm.io/xorm"
)

const maxCMBStatementBytes = 20 << 20

var installmentPhasePattern = regexp.MustCompile(`第\s*(\d+)\s*/\s*(\d+)\s*期`)

func parseInstallmentPhase(description string) (current, total int, ok bool) {
	matches := installmentPhasePattern.FindStringSubmatch(description)
	if len(matches) != 3 {
		return 0, 0, false
	}
	current, currentErr := strconv.Atoi(matches[1])
	total, totalErr := strconv.Atoi(matches[2])
	if currentErr != nil || totalErr != nil || current < 1 || total < 1 || current > total {
		return 0, 0, false
	}
	return current, total, true
}

// HengcaiApi contains the private Hengcai extensions.  It is deliberately a
// normal ezBookkeeping API singleton, so all routes inherit its JWT and API
// token middleware and all rows are scoped by the current uid.
type HengcaiApi struct{ ApiUsingConfig }

var Hengcai = &HengcaiApi{ApiUsingConfig: ApiUsingConfig{container: settings.Container}}

func hcError(err error) *errs.Error {
	if err == nil {
		return nil
	}
	base := errs.ErrIncompleteOrIncorrectSubmission
	return errs.New(base.Category, base.SubCategory, base.Index, base.HttpStatusCode, err.Error(), err)
}

func postableCategories(categories []*models.TransactionCategory) []*models.TransactionCategory {
	categoryMap := make(map[int64]*models.TransactionCategory, len(categories))
	for _, category := range categories {
		if category != nil {
			categoryMap[category.CategoryId] = category
		}
	}
	result := make([]*models.TransactionCategory, 0, len(categories))
	for _, category := range categories {
		if category == nil || category.Hidden || category.ParentCategoryId == models.LevelOneTransactionCategoryParentId {
			continue
		}
		parent := categoryMap[category.ParentCategoryId]
		if parent == nil || parent.Hidden {
			continue
		}
		result = append(result, category)
	}
	return result
}

func statementTransactionTime(postedDate string) (int64, error) {
	parsed, err := time.Parse("2006-01-02", postedDate)
	if err != nil {
		return 0, err
	}
	return utils.GetMinTransactionTimeFromUnixTime(parsed.Unix()), nil
}

func (a *HengcaiApi) previewCMB(c *core.WebContext) (any, *errs.Error) {
	statement, _, err := readCMBUpload(c)
	if err != nil {
		return nil, hcError(err)
	}
	return map[string]any{"statement": statement, "preview_only": true}, nil
}

func readCMBUpload(c *core.WebContext) (statementparser.CreditCardStatement, []byte, error) {
	if c.Request == nil {
		return statementparser.CreditCardStatement{}, nil, errors.New("request is empty")
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		return statementparser.CreditCardStatement{}, nil, errors.New("请使用 multipart 字段 file 上传招商银行 PDF")
	}
	defer file.Close()
	if header.Size > maxCMBStatementBytes {
		return statementparser.CreditCardStatement{}, nil, errors.New("账单文件不能超过 20 MB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCMBStatementBytes+1))
	if err != nil {
		return statementparser.CreditCardStatement{}, nil, err
	}
	if len(data) > maxCMBStatementBytes {
		return statementparser.CreditCardStatement{}, nil, errors.New("账单文件不能超过 20 MB")
	}
	statement, err := statementparser.ParseCMBCreditCardPDF(bytes.NewReader(data), int64(len(data)))
	return statement, data, err
}

func (a *HengcaiApi) confirmCMB(c *core.WebContext) (any, *errs.Error) {
	statement, _, err := readCMBUpload(c)
	if err != nil {
		return nil, hcError(err)
	}
	if statement.NeedsReview || !statement.BalanceValid || !statement.SummaryValid {
		return nil, errs.ErrOperationFailed
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("account_id")), 10, 64)
	if err != nil || accountID <= 0 {
		return nil, hcError(errors.New("account_id 必须是有效的 ezBookkeeping 信用卡账户"))
	}
	uid := c.GetCurrentUid()
	now := time.Now().Unix()
	raw, _ := json.Marshal(statement)
	validation, _ := json.Marshal(statement.ValidationErrors)
	model := &hengcai.StatementImport{
		Uid: uid, Provider: "CMB", AccountId: accountID,
		StatementDate: statement.StatementDate, PeriodStart: statement.StatementPeriodStart,
		PeriodEnd: statement.StatementPeriodEnd, Currency: statement.Currency,
		OpeningBalanceMinor: statement.OpeningBalanceMinor, ClosingBalanceMinor: statement.ClosingBalanceMinor,
		TotalDebitMinor: statement.TotalDebitMinor, TotalCreditMinor: statement.TotalCreditMinor,
		ArtifactHash: statement.ArtifactSHA256, Status: "IMPORTED", BalanceValid: statement.BalanceValid,
		SummaryValid: statement.SummaryValid, ValidationErrors: string(validation), RawPayload: string(raw), CreatedUnixTime: now,
	}
	lines := make([]*hengcai.StatementLine, 0, len(statement.Lines))
	for _, line := range statement.Lines {
		lineRaw, _ := json.Marshal(line)
		transactionDate := ""
		if line.TransactionDate != nil {
			transactionDate = *line.TransactionDate
		}
		lines = append(lines, &hengcai.StatementLine{
			Uid: uid, LineNumber: line.LineNumber, TransactionDate: transactionDate, PostedDate: line.PostedDate,
			Description: line.Description, AmountMinor: line.AmountMinor, SignedAmountMinor: line.SignedAmountMinor,
			Direction: line.Direction, Currency: statement.Currency, CardLastFour: line.CardLastFour,
			Section: line.Section, LineKind: line.LineKind, AccountingTreatment: line.AccountingTreatment,
			SettlesPriorStatement: line.SettlesPriorStatement, RequiresReview: line.RequiresExpenseReview,
			LineHash: line.LineHash, Status: "UNMATCHED", RawPayload: string(lineRaw), ConfidenceBps: 0,
		})
	}
	err = datastore.Container.UserDataStore.DoTransaction(uid, c, func(sess *xorm.Session) error {
		if _, err := sess.Insert(model); err != nil {
			return err
		}
		for _, line := range lines {
			line.StatementId = model.Id
			if _, err := sess.Insert(line); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"id": model.Id, "line_count": len(lines), "status": model.Status}, nil
}

func (a *HengcaiApi) listStatements(c *core.WebContext) (any, *errs.Error) {
	var rows []*hengcai.StatementImport
	err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ?", c.GetCurrentUid()).Desc("id").Find(&rows)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return rows, nil
}

func (a *HengcaiApi) getStatementLines(c *core.WebContext) (any, *errs.Error) {
	id, err := strconv.ParseInt(c.Query("statement_id"), 10, 64)
	if err != nil || id <= 0 {
		return nil, hcError(errors.New("statement_id 无效"))
	}
	var rows []*hengcai.StatementLine
	err = datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ? AND statement_id = ?", c.GetCurrentUid(), id).Asc("line_number").Find(&rows)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return rows, nil
}

func (a *HengcaiApi) classifyStatement(c *core.WebContext) (any, *errs.Error) {
	id, err := strconv.ParseInt(c.PostForm("statement_id"), 10, 64)
	if err != nil || id <= 0 {
		return nil, hcError(errors.New("statement_id 无效"))
	}
	log.Infof(c, "[hengcai.classifyStatement] user \"uid:%d\" requests classification for statement \"id:%d\", use_ai=%q", c.GetCurrentUid(), id, c.PostForm("use_ai"))
	var lines []*hengcai.StatementLine
	sess := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid())
	if err := sess.Where("uid = ? AND statement_id = ?", c.GetCurrentUid(), id).Find(&lines); err != nil {
		return nil, errs.ErrOperationFailed
	}
	usedUserAI := false
	uid := c.GetCurrentUid()
	useAI := strings.EqualFold(c.PostForm("use_ai"), "true")
	categories := make([]*models.TransactionCategory, 0)
	if expenseCategories, categoryErr := services.TransactionCategories.GetAllCategoriesByUid(c, uid, models.CATEGORY_TYPE_EXPENSE, -1); categoryErr == nil {
		categories = append(categories, expenseCategories...)
	}
	if incomeCategories, categoryErr := services.TransactionCategories.GetAllCategoriesByUid(c, uid, models.CATEGORY_TYPE_INCOME, -1); categoryErr == nil {
		categories = append(categories, incomeCategories...)
	}
	categories = postableCategories(categories)

	// 1) 优先参考账单自带的“交易分类”做确定性映射，不依赖 AI
	if len(categories) > 0 {
		if err := a.applyStatementCategories(sess, uid, lines, categories); err != nil {
			return nil, errs.ErrOperationFailed
		}
		if err := a.applySpecialStatementTreatments(sess, uid, id, lines, categories); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}

	// 2) 剩余未分类的支出/收入流水交给用户 AI
	if useAI && len(categories) > 0 {
		var remaining []*hengcai.StatementLine
		if err := sess.Where("uid = ? AND statement_id = ?", uid, id).Find(&remaining); err != nil {
			return nil, errs.ErrOperationFailed
		}
		aiLines := make([]*hengcai.StatementLine, 0, len(remaining))
		for _, line := range remaining {
			// Deterministic bill-category mapping and manual confirmations are
			// authoritative; AI only fills/refines the rest so re-runs converge.
			if line.Status == "POSTED" || line.Status == "EVIDENCE" || line.MatchedTransactionId > 0 || line.Classification == "人工确认" || line.Classification == "账单分类" {
				continue
			}
			if line.CounterpartyType == "PERSON" {
				continue // Personal counterparties are never auto-classified into a postable state.
			}
			if line.LineKind == statementparser.LineKindPurchase || line.LineKind == statementparser.LineKindRefund || line.LineKind == lineKindIncome {
				aiLines = append(aiLines, line)
			}
		}
		if len(aiLines) > 0 {
			var aiErr error
			usedUserAI, aiErr = a.classifyLinesWithUserAI(c, aiLines, categories)
			if aiErr != nil {
				return nil, hcError(aiErr)
			}
		}
	}
	// When ezBookkeeping's configured text model is enabled, ask it for a
	// strict JSON classification first. Any unavailable/malformed model output
	// falls back to the safe review-required state below.
	if !usedUserAI && strings.EqualFold(c.PostForm("use_ai"), "true") && a.CurrentConfig().TextRecognitionLLMConfig != nil && a.CurrentConfig().TransactionFromAITextRecognition {
		categories, categoryErr := services.TransactionCategories.GetAllCategoriesByUid(c, c.GetCurrentUid(), models.CATEGORY_TYPE_EXPENSE, -1)
		if categoryErr == nil && len(categories) > 0 {
			categoryLines := make([]string, 0, len(categories))
			for _, category := range categories {
				categoryLines = append(categoryLines, fmt.Sprintf("%d=%s", category.CategoryId, category.Name))
			}
			prompt := "将以下账单消费流水归类到给定支出分类，只返回 JSON 数组 [{\"line_hash\":string,\"category_id\":number,\"confidence_bps\":number}]。分类列表：" + strings.Join(categoryLines, ",") + "。流水："
			for _, line := range lines {
				if line.LineKind == statementparser.LineKindPurchase {
					prompt += fmt.Sprintf("[%s]%s;", line.LineHash, line.Description)
				}
			}
			response, callErr := llm.Container.GetJsonResponseByTextRecognitionModel(c, c.GetCurrentUid(), a.CurrentConfig(), &data.LargeLanguageModelRequest{Stream: false, SystemPrompt: "你是严谨的个人记账分类器。不得编造分类 ID。", UserPrompt: []byte(prompt), UserPromptType: data.LARGE_LANGUAGE_MODEL_REQUEST_PROMPT_TYPE_TEXT})
			if callErr == nil && response != nil {
				var suggestions []struct {
					LineHash      string `json:"line_hash"`
					CategoryId    int64  `json:"category_id"`
					ConfidenceBps int    `json:"confidence_bps"`
				}
				if json.Unmarshal([]byte(response.Content), &suggestions) == nil {
					for _, suggestion := range suggestions {
						if suggestion.CategoryId <= 0 {
							continue
						}
						_, _ = sess.Where("uid = ? AND statement_id = ? AND line_hash = ? AND counterparty_type <> ?", c.GetCurrentUid(), id, suggestion.LineHash, "PERSON").Cols("category_id", "confidence_bps", "status").Update(&hengcai.StatementLine{CategoryId: suggestion.CategoryId, ConfidenceBps: suggestion.ConfidenceBps, Status: "CLASSIFIED"})
					}
				}
			}
		}
	}
	// Reload the latest line state so the deterministic fallback below only
	// fills lines that were NOT classified by the AI.
	var latestLines []*hengcai.StatementLine
	if err := sess.Where("uid = ? AND statement_id = ?", c.GetCurrentUid(), id).Find(&latestLines); err != nil {
		return nil, errs.ErrOperationFailed
	}
	lines = latestLines
	for _, line := range lines {
		if line.CategoryId > 0 {
			continue
		}
		// Deterministic fallback classification is useful even when no LLM is
		// configured; the user can override category_id before posting.
		line.Classification = "待确认支出"
		if line.LineKind == lineKindIncome {
			line.Classification = "待确认收入"
		}
		line.ConfidenceBps = 2500
		if _, err := sess.ID(line.Id).Cols("classification", "confidence_bps").Update(line); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}
	return lines, nil
}

// applySpecialStatementTreatments resolves statement rows whose accounting
// treatment is deterministic. Interest is OPEX, subsidies reduce that OPEX,
// while repayments/setup rows are settlement evidence rather than expenses.
// Installment principal remains pending until it is linked to CAPEX.
func (a *HengcaiApi) applySpecialStatementTreatments(sess *xorm.Session, uid, statementID int64, lines []*hengcai.StatementLine, categories []*models.TransactionCategory) error {
	interestCategoryID := int64(0)
	for _, category := range categories {
		if category != nil && category.Type == models.CATEGORY_TYPE_EXPENSE && normalizeCategoryName(category.Name) == normalizeCategoryName("利息支出") {
			interestCategoryID = category.CategoryId
			break
		}
	}
	settlements := make(map[int64]bool)
	var linked []*hengcai.CapexInstallmentSettlement
	if err := sess.Where("uid = ?", uid).Find(&linked); err != nil {
		return err
	}
	for _, settlement := range linked {
		settlements[settlement.StatementLineId] = true
	}
	for _, line := range lines {
		if line.Status == "POSTED" || line.Classification == "人工确认" {
			continue
		}
		update := &hengcai.StatementLine{}
		columns := []string{"classification", "status"}
		switch line.LineKind {
		case statementparser.LineKindInstallmentInterest:
			if interestCategoryID == 0 {
				continue
			}
			update.CategoryId = interestCategoryID
			update.Classification = "规则分类：分期利息"
			update.ConfidenceBps = 10000
			update.Status = "CLASSIFIED"
			columns = append(columns, "category_id", "confidence_bps")
		case statementparser.LineKindInterestSubsidy:
			if interestCategoryID == 0 {
				continue
			}
			update.CategoryId = interestCategoryID
			update.Classification = "规则分类：利息冲减"
			update.ConfidenceBps = 10000
			update.Status = "CLASSIFIED"
			columns = append(columns, "category_id", "confidence_bps")
		case statementparser.LineKindInstallmentPrincipal:
			update.Classification = "待关联 CAPEX"
			update.Status = "REVIEW"
			if settlements[line.Id] {
				update.Classification = "已关联 CAPEX 分期"
				update.Status = "CAPEX_LINKED"
			}
		case statementparser.LineKindRepayment:
			update.Classification = "信用卡还款结算证据"
			update.Status = "EVIDENCE"
		case statementparser.LineKindInstallmentSetup:
			update.Classification = "分期计划调整证据"
			update.Status = "EVIDENCE"
		default:
			continue
		}
		if _, err := sess.Where("uid = ? AND statement_id = ? AND id = ?", uid, statementID, line.Id).Cols(columns...).Update(update); err != nil {
			return err
		}
	}
	return nil
}

// statementCategoryAliases maps common statement-provided category names
// (e.g. Alipay's 交易分类) to user category name keywords so a deterministic
// first-pass mapping works even when the two taxonomies use different words.
var statementCategoryAliases = map[string][]string{
	"餐饮美食": {"食品", "餐饮", "水果", "零食", "饮料", "咖啡", "外卖"},
	"服饰装扮": {"服饰", "衣服", "饰品", "化妆品", "美容"},
	"日用百货": {"家居", "日用", "百货", "家清", "生活用品"},
	"商业服务": {"服务", "手续费", "杂项", "其他", "打印", "快递"},
	"交通出行": {"交通", "出行", "打车", "公共交通", "加油", "停车"},
	"医疗健康": {"医疗", "健康", "药品", "检查", "药店"},
	"教育培训": {"教育", "培训", "学习", "课程", "考试", "书报"},
	"文化休闲": {"休闲", "娱乐", "电影", "演出", "运动", "健身", "游戏", "旅游", "会员"},
	"数码电器": {"电子", "数码", "家电", "手机"},
	"家居家装": {"家居", "住宅", "装修", "家具"},
	"充值缴费": {"充值", "缴费", "话费", "网费", "水电", "燃气"},
	"保险":   {"保险"},
	"收入":   {"其他", "收入", "工资", "奖金", "利息", "红包"},
	"其他":   {"其他"},
}

// applyStatementCategories deterministically maps the statement-provided
// transaction category (e.g. Alipay's 交易分类 column) to the user's own
// category by exact name, alias keyword or containment. Deterministic rules
// win over AI suggestions, but manual confirmations are always preserved.
func (a *HengcaiApi) applyStatementCategories(sess *xorm.Session, uid int64, lines []*hengcai.StatementLine, categories []*models.TransactionCategory) error {
	expenseNames := make(map[string]int64, len(categories))
	incomeNames := make(map[string]int64, len(categories))
	for _, category := range categories {
		name := normalizeCategoryName(category.Name)
		if name == "" {
			continue
		}
		if category.Type == models.CATEGORY_TYPE_INCOME {
			incomeNames[name] = category.CategoryId
		} else {
			expenseNames[name] = category.CategoryId
		}
	}
	var bestContainingName func(needle string, names map[string]int64) int64
	matchName := func(name string, names map[string]int64) int64 {
		if id, ok := names[name]; ok {
			return id
		}
		// alias keywords in fixed order; prefer the shortest matching category
		// name so sub-categories win over their parents deterministically
		for _, keyword := range statementCategoryAliases[name] {
			if id := bestContainingName(keyword, names); id != 0 {
				return id
			}
		}
		// containment fallback: one side contains the other (length >= 2)
		return bestContainingName(name, names)
	}
	bestContainingName = func(needle string, names map[string]int64) int64 {
		candidates := make([]string, 0, len(names))
		for key := range names {
			if len(key) >= 2 && len(needle) >= 2 && (strings.Contains(key, needle) || strings.Contains(needle, key)) {
				candidates = append(candidates, key)
			}
		}
		if len(candidates) == 0 {
			return 0
		}
		sort.Slice(candidates, func(i, j int) bool {
			if len(candidates[i]) != len(candidates[j]) {
				return len(candidates[i]) < len(candidates[j])
			}
			return candidates[i] < candidates[j]
		})
		return names[candidates[0]]
	}
	for _, line := range lines {
		if line.Classification == "人工确认" {
			continue
		}
		name := normalizeCategoryName(line.StatementCategory)
		if name == "" {
			continue
		}
		var categoryID int64
		switch line.LineKind {
		case statementparser.LineKindPurchase, statementparser.LineKindRefund:
			categoryID = matchName(name, expenseNames)
		case lineKindIncome:
			categoryID = matchName(name, incomeNames)
		default:
			continue
		}
		if categoryID <= 0 {
			continue
		}
		if _, err := sess.Where("uid = ? AND id = ?", uid, line.Id).Cols("category_id", "classification", "confidence_bps", "status").Update(&hengcai.StatementLine{CategoryId: categoryID, Classification: "账单分类", ConfidenceBps: 9500, Status: "CLASSIFIED"}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCategoryName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), ""))
}

func (a *HengcaiApi) updateLineClassification(c *core.WebContext) (any, *errs.Error) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		CategoryId int64  `json:"category_id,string"`
		Label      string `json:"label"`
		Confidence int    `json:"confidence_bps"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || id <= 0 {
		return nil, hcError(errors.New("分类请求无效"))
	}
	uid := c.GetCurrentUid()
	var currentLine hengcai.StatementLine
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("id = ? AND uid = ?", id, uid).Get(&currentLine); err != nil || !ok {
		return nil, hcError(errors.New("账单流水不存在"))
	}
	var category models.TransactionCategory
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("category_id = ? AND uid = ? AND deleted = ?", input.CategoryId, uid, false).Get(&category); err != nil || !ok {
		return nil, hcError(errors.New("分类不存在"))
	}
	if category.Hidden || category.ParentCategoryId == models.LevelOneTransactionCategoryParentId {
		return nil, hcError(errors.New("请选择可入账的二级分类"))
	}
	var parentCategory models.TransactionCategory
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("category_id = ? AND uid = ? AND deleted = ?", category.ParentCategoryId, uid, false).Get(&parentCategory); err != nil || !ok || parentCategory.Hidden {
		return nil, hcError(errors.New("请选择可入账的二级分类"))
	}
	expectedType := models.CATEGORY_TYPE_EXPENSE
	if currentLine.LineKind == lineKindIncome {
		expectedType = models.CATEGORY_TYPE_INCOME
	}
	if category.Type != expectedType {
		return nil, hcError(errors.New("分类类型与流水收支方向不一致"))
	}
	status := "CLASSIFIED"
	if currentLine.MatchType == "PLATFORM_UNRESOLVED" {
		// Choosing a category does not resolve whether a platform-looking bank
		// row duplicates an Alipay/WeChat transaction.
		status = "REVIEW"
	}
	line := &hengcai.StatementLine{CategoryId: input.CategoryId, Classification: strings.TrimSpace(input.Label), ConfidenceBps: input.Confidence, Status: status}
	columns := []string{"category_id", "classification", "confidence_bps", "status"}
	// A category selected in the workspace is an explicit manual decision.  The
	// old person-counterparty review marker must not reappear on the next
	// dashboard refresh after that decision has been saved.
	if currentLine.CounterpartyType == "PERSON" && currentLine.MatchType != "PLATFORM_UNRESOLVED" {
		line.RequiresReview = false
		line.ReviewReason = ""
		line.MatchType = "MANUAL_CLASSIFICATION"
		line.CoverageState = "VERIFIED"
		columns = append(columns, "requires_review", "review_reason", "match_type", "coverage_state")
	}
	_, err := datastore.Container.UserDataStore.Query(c, uid).Where("id = ? AND uid = ?", id, uid).Cols(columns...).Update(line)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"id": id, "category_id": input.CategoryId, "label": input.Label, "confidence_bps": input.Confidence}, nil
}

// updateLineAsRefund turns a positive bank credit into a refund treatment.
// Refunds are deliberately not a transaction category: the user chooses the
// original expense category, while the accounting treatment controls the
// signed posting amount and keeps the value out of income totals.
func (a *HengcaiApi) updateLineAsRefund(c *core.WebContext) (any, *errs.Error) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		CategoryId int64 `json:"category_id,string"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || id <= 0 || input.CategoryId <= 0 {
		return nil, hcError(errors.New("退款分类请求无效"))
	}
	uid := c.GetCurrentUid()
	var currentLine hengcai.StatementLine
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("id = ? AND uid = ?", id, uid).Get(&currentLine); err != nil || !ok {
		return nil, hcError(errors.New("账单流水不存在"))
	}
	if currentLine.LineKind != lineKindIncome {
		return nil, hcError(errors.New("只有收入流水可以转为退款"))
	}
	if currentLine.Status == "POSTED" || currentLine.Status == "EVIDENCE" || currentLine.MatchedTransactionId > 0 {
		return nil, hcError(errors.New("已入账、已作为结算证据或已匹配的流水不能转为退款"))
	}
	var category models.TransactionCategory
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("category_id = ? AND uid = ? AND deleted = ?", input.CategoryId, uid, false).Get(&category); err != nil || !ok {
		return nil, hcError(errors.New("退款对应的支出分类不存在"))
	}
	if category.Hidden || category.ParentCategoryId == models.LevelOneTransactionCategoryParentId || category.Type != models.CATEGORY_TYPE_EXPENSE {
		return nil, hcError(errors.New("退款必须选择可入账的二级支出分类"))
	}
	var parentCategory models.TransactionCategory
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("category_id = ? AND uid = ? AND deleted = ?", category.ParentCategoryId, uid, false).Get(&parentCategory); err != nil || !ok || parentCategory.Hidden {
		return nil, hcError(errors.New("退款对应的支出分类无效"))
	}
	line := &hengcai.StatementLine{
		LineKind:            statementparser.LineKindRefund,
		AccountingTreatment: "REFUND_MATCH_CANDIDATE",
		SignedAmountMinor:   -abs64(currentLine.AmountMinor),
		CategoryId:          input.CategoryId,
		Classification:      "人工确认退款",
		ConfidenceBps:       10000,
		RequiresReview:      false,
		ReviewReason:        "",
		Status:              "CLASSIFIED",
		MatchType:           "MANUAL_CLASSIFICATION",
		CoverageState:       "VERIFIED",
	}
	_, err := datastore.Container.UserDataStore.Query(c, uid).Where("id = ? AND uid = ?", id, uid).Cols("line_kind", "accounting_treatment", "signed_amount_minor", "category_id", "classification", "confidence_bps", "requires_review", "review_reason", "status", "match_type", "coverage_state").Update(line)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"id": id, "line_kind": statementparser.LineKindRefund, "signed_amount_minor": line.SignedAmountMinor, "category_id": input.CategoryId, "accounting_treatment": line.AccountingTreatment}, nil
}

func (a *HengcaiApi) linkLineToCapexInstallment(c *core.WebContext) (any, *errs.Error) {
	lineID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		InstallmentId int64 `json:"installment_id,string"`
	}
	if c.ShouldBindJSON(&input) != nil || lineID <= 0 || input.InstallmentId <= 0 {
		return nil, hcError(errors.New("CAPEX 关联请求无效"))
	}
	uid := c.GetCurrentUid()
	var line hengcai.StatementLine
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, lineID).Get(&line); err != nil || !ok {
		return nil, hcError(errors.New("账单流水不存在"))
	}
	if line.LineKind != statementparser.LineKindInstallmentPrincipal {
		return nil, hcError(errors.New("只有分期本金流水可以关联 CAPEX"))
	}
	if line.Status == "POSTED" {
		return nil, hcError(errors.New("已入账的分期本金不能重新关联"))
	}
	var installment hengcai.CapexInstallment
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, input.InstallmentId).Get(&installment); err != nil || !ok {
		return nil, hcError(errors.New("CAPEX 分期不存在"))
	}
	currentNo, totalCount, parsed := parseInstallmentPhase(line.Description)
	if !parsed {
		return nil, hcError(errors.New("账单流水缺少有效的分期期次，无法安全关联"))
	}
	var purchase hengcai.CapexPurchase
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, installment.PurchaseId).Get(&purchase); err != nil || !ok {
		return nil, hcError(errors.New("CAPEX 项目不存在"))
	}
	if installment.InstallmentNo != currentNo || purchase.InstallmentCount != totalCount {
		return nil, hcError(fmt.Errorf("请选择第 %d/%d 期对应的 CAPEX 计划", currentNo, totalCount))
	}
	occupied, occupiedErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND installment_id = ? AND posted = ? AND statement_line_id <> ?", uid, installment.Id, true, lineID).Count(&hengcai.CapexInstallmentSettlement{})
	if occupiedErr != nil {
		return nil, errs.ErrOperationFailed
	}
	if occupied > 0 {
		return nil, hcError(errors.New("该 CAPEX 期次已被其他账单本金结算"))
	}
	var settlement hengcai.CapexInstallmentSettlement
	has, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND statement_line_id = ?", uid, lineID).Get(&settlement)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	now := time.Now().Unix()
	settlement.Uid = uid
	settlement.StatementLineId = lineID
	settlement.InstallmentId = input.InstallmentId
	settlement.PrincipalMinor = abs64(line.AmountMinor)
	settlement.UpdatedUnixTime = now
	if has {
		if settlement.Posted {
			return nil, hcError(errors.New("已结算的 CAPEX 关联不能修改"))
		}
		if _, err = datastore.Container.UserDataStore.Query(c, uid).ID(settlement.Id).Cols("installment_id", "principal_minor", "updated_unix_time").Update(&settlement); err != nil {
			return nil, errs.ErrOperationFailed
		}
	} else {
		settlement.CreatedUnixTime = now
		if _, err = datastore.Container.UserDataStore.Query(c, uid).Insert(&settlement); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}
	if _, err = datastore.Container.UserDataStore.Query(c, uid).ID(lineID).Where("uid = ?", uid).Cols("classification", "status").Update(&hengcai.StatementLine{Classification: "已关联 CAPEX 分期", Status: "CAPEX_LINKED"}); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"statement_line_id": lineID, "installment_id": input.InstallmentId, "principal_minor": settlement.PrincipalMinor}, nil
}

func (a *HengcaiApi) postStatement(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		StatementId int64  `json:"statement_id,string"`
		Month       string `json:"month"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.StatementId <= 0 {
		return nil, hcError(errors.New("statement_id 无效"))
	}
	uid := c.GetCurrentUid()
	var statement hengcai.StatementImport
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("id = ? AND uid = ?", input.StatementId, uid).Get(&statement); err != nil || !ok {
		return nil, errs.ErrOperationFailed
	}
	if input.Month != "" && !statementOverlapsMonth(&statement, input.Month) {
		return nil, hcError(errors.New("账单期间与当前对账月份不一致"))
	}
	postingMonth := statementMonth(&statement)
	if postingMonth == "" {
		postingMonth = input.Month
	}
	var lines []*hengcai.StatementLine
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND statement_id = ?", uid, input.StatementId).Find(&lines); err != nil {
		return nil, errs.ErrOperationFailed
	}
	allCategories := make([]*models.TransactionCategory, 0)
	if categories, err := services.TransactionCategories.GetAllCategoriesByUid(c, uid, 0, -1); err == nil {
		allCategories = categories
	} else {
		return nil, errs.ErrOperationFailed
	}
	categoryMap := make(map[int64]*models.TransactionCategory, len(allCategories))
	for _, category := range allCategories {
		categoryMap[category.CategoryId] = category
	}

	type postPlan struct {
		line        *hengcai.StatementLine
		transaction *models.Transaction
		settlement  *hengcai.CapexInstallmentSettlement
		installment *hengcai.CapexInstallment
	}
	plans := make([]postPlan, 0, len(lines))
	newTransactions := make([]*models.Transaction, 0, len(lines))
	for _, line := range lines {
		if line.Status == "EVIDENCE" || line.Status == "POSTED" {
			continue
		}
		if line.LineKind == statementparser.LineKindInstallmentPrincipal {
			var settlement hengcai.CapexInstallmentSettlement
			if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND statement_line_id = ?", uid, line.Id).Get(&settlement); err != nil {
				return nil, errs.ErrOperationFailed
			} else if !ok {
				return nil, hcError(fmt.Errorf("流水 %d 的分期本金尚未关联 CAPEX", line.LineNumber))
			}
			var installment hengcai.CapexInstallment
			if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, settlement.InstallmentId).Get(&installment); err != nil {
				return nil, errs.ErrOperationFailed
			} else if !ok {
				return nil, hcError(fmt.Errorf("流水 %d 关联的 CAPEX 分期不存在", line.LineNumber))
			}
			plans = append(plans, postPlan{line: line, settlement: &settlement, installment: &installment})
			continue
		}
		if line.LineKind != statementparser.LineKindPurchase && line.LineKind != statementparser.LineKindRefund && line.LineKind != lineKindIncome && line.LineKind != statementparser.LineKindInstallmentInterest && line.LineKind != statementparser.LineKindInterestSubsidy {
			continue
		}
		if line.Status == "REVIEW" || line.MatchType == "AMBIGUOUS" {
			return nil, hcError(fmt.Errorf("流水 %d 存在匹配冲突，请先人工确认", line.LineNumber))
		}
		if line.CategoryId <= 0 {
			return nil, hcError(fmt.Errorf("流水 %d 尚未完成分类", line.LineNumber))
		}
		category := categoryMap[line.CategoryId]
		if category == nil || category.Hidden || category.ParentCategoryId == models.LevelOneTransactionCategoryParentId {
			return nil, hcError(fmt.Errorf("流水 %d 使用了不可入账的一级分类，请重新生成或人工选择二级分类", line.LineNumber))
		}
		parentCategory := categoryMap[category.ParentCategoryId]
		if parentCategory == nil || parentCategory.Hidden {
			return nil, hcError(fmt.Errorf("流水 %d 使用了不可入账的一级分类，请重新生成或人工选择二级分类", line.LineNumber))
		}
		expectedCategoryType := models.CATEGORY_TYPE_EXPENSE
		if line.LineKind == lineKindIncome {
			expectedCategoryType = models.CATEGORY_TYPE_INCOME
		}
		if category.Type != expectedCategoryType {
			return nil, hcError(fmt.Errorf("流水 %d 的分类类型与收支方向不一致", line.LineNumber))
		}
		if line.MatchedTransactionId > 0 {
			plans = append(plans, postPlan{line: line})
			continue
		}
		transactionTime, parseErr := statementTransactionTime(line.PostedDate)
		if parseErr != nil {
			return nil, hcError(fmt.Errorf("流水 %d 的交易日期无效", line.LineNumber))
		}
		txType := models.TRANSACTION_DB_TYPE_EXPENSE
		amount := line.AmountMinor
		if line.LineKind == lineKindIncome {
			txType = models.TRANSACTION_DB_TYPE_INCOME
			amount = abs64(amount)
		} else if line.LineKind == statementparser.LineKindRefund {
			txType = models.TRANSACTION_DB_TYPE_EXPENSE
			amount = -abs64(amount)
		} else if line.LineKind == statementparser.LineKindInterestSubsidy {
			txType = models.TRANSACTION_DB_TYPE_EXPENSE
			amount = -abs64(amount)
		}
		transaction := &models.Transaction{Uid: uid, Deleted: false, Type: txType, CategoryId: line.CategoryId, AccountId: statement.AccountId, TransactionTime: transactionTime, TimezoneUtcOffset: 480, Amount: amount, Comment: line.Description, CreatedIp: c.ClientIP()}
		newTransactions = append(newTransactions, transaction)
		plans = append(plans, postPlan{line: line, transaction: transaction})
	}
	now := time.Now().Unix()
	err := services.Transactions.BatchCreateTransactionsWithFinalizer(c, uid, newTransactions, map[int][]int64{}, nil, func(sess *xorm.Session) error {
		var metadataErr error
		for _, plan := range plans {
			if plan.settlement != nil && plan.installment != nil {
				if !plan.settlement.Posted {
					actualPaid := plan.installment.ActualPaidMinor + plan.settlement.PrincipalMinor
					status := "PARTIALLY_PAID"
					if actualPaid >= plan.installment.PrincipalMinor {
						status = "PAID"
					}
					if _, metadataErr = sess.ID(plan.installment.Id).Where("uid = ?", uid).Cols("actual_paid_minor", "status").Update(&hengcai.CapexInstallment{ActualPaidMinor: actualPaid, Status: status}); metadataErr != nil {
						return metadataErr
					}
					if _, metadataErr = sess.ID(plan.settlement.Id).Where("uid = ?", uid).Cols("posted", "updated_unix_time").Update(&hengcai.CapexInstallmentSettlement{Posted: true, UpdatedUnixTime: now}); metadataErr != nil {
						return metadataErr
					}
				}
				if _, metadataErr = sess.ID(plan.line.Id).Where("uid = ?", uid).Cols("status").Update(&hengcai.StatementLine{Status: "POSTED"}); metadataErr != nil {
					return metadataErr
				}
				continue
			}
			transactionID := plan.line.MatchedTransactionId
			categorySource := "MANUAL"
			evidenceType := "SETTLEMENT"
			matchScore := plan.line.MatchScoreBps
			if plan.transaction != nil {
				transactionID = plan.transaction.TransactionId
				categorySource = "CONFIRMED"
				evidenceType = "MERCHANT"
				matchScore = 10000
			}
			// TransactionOrigin identifies the one statement line that created the
			// core transaction. A line matched to an existing transaction is extra
			// evidence, not a second origin; inserting another origin would violate
			// the uid/transaction_id uniqueness invariant.
			if plan.transaction != nil {
				var originCount int64
				originCount, metadataErr = sess.Where("uid = ? AND statement_line_id = ?", uid, plan.line.Id).Count(&hengcai.TransactionOrigin{})
				if metadataErr != nil {
					return metadataErr
				}
				if originCount == 0 {
					if _, metadataErr = sess.Insert(&hengcai.TransactionOrigin{Uid: uid, TransactionId: transactionID, StatementLineId: plan.line.Id, Provider: statement.Provider, CategorySource: categorySource, VerificationState: "VERIFIED", CreatedUnixTime: now}); metadataErr != nil {
						return metadataErr
					}
				}
			}
			var evidenceCount int64
			evidenceCount, metadataErr = sess.Where("uid = ? AND statement_line_id = ?", uid, plan.line.Id).Count(&hengcai.TransactionEvidence{})
			if metadataErr != nil {
				return metadataErr
			}
			if evidenceCount == 0 {
				if _, metadataErr = sess.Insert(&hengcai.TransactionEvidence{Uid: uid, TransactionId: transactionID, StatementLineId: plan.line.Id, EvidenceType: evidenceType, MerchantChannel: plan.line.MerchantChannel, FundingSource: plan.line.FundingSource, VerificationState: "VERIFIED", MatchScoreBps: matchScore, CreatedUnixTime: now}); metadataErr != nil {
					return metadataErr
				}
			}
			if _, metadataErr = sess.ID(plan.line.Id).Where("uid = ?", uid).Cols("status", "matched_transaction_id").Update(&hengcai.StatementLine{Status: "POSTED", MatchedTransactionId: transactionID}); metadataErr != nil {
				return metadataErr
			}
		}
		if _, metadataErr = sess.ID(input.StatementId).Where("uid = ?", uid).Cols("status", "coverage_status", "covered_until").Update(&hengcai.StatementImport{Status: "POSTED", CoverageStatus: "VERIFIED", CoveredUntil: statement.PeriodEnd}); metadataErr != nil {
			return metadataErr
		}
		_, metadataErr = sess.Where("uid = ? AND statement_id = ?", uid, input.StatementId).Cols("status").Update(&hengcai.TransactionCoverage{Status: "VERIFIED"})
		return metadataErr
	})
	if err != nil {
		log.Errorf(c, "[hengcai.postStatement] failed to atomically post statement %d: %s", input.StatementId, err.Error())
		return nil, hcError(fmt.Errorf("账单入账失败：%s", err.Error()))
	}
	return map[string]any{"statement_id": input.StatementId, "posted": len(plans), "month": postingMonth, "view_month": input.Month}, nil
}

func restoredStatementLineState(line *hengcai.StatementLine, createdTransaction bool) (status string, matchedTransactionID int64) {
	if line == nil {
		return "UNMATCHED", 0
	}
	if line.LineKind == statementparser.LineKindInstallmentPrincipal {
		return "CAPEX_LINKED", 0
	}
	if !createdTransaction && line.MatchedTransactionId > 0 {
		return "MATCHED", line.MatchedTransactionId
	}
	if line.CategoryId > 0 {
		return "CLASSIFIED", 0
	}
	return "UNMATCHED", 0
}

func reverseStatementCreatedTransaction(sess *xorm.Session, uid int64, transactionID int64, now int64) (bool, error) {
	var transaction models.Transaction
	ok, err := sess.Where("uid = ? AND transaction_id = ?", uid, transactionID).Get(&transaction)
	if err != nil || !ok {
		return false, err
	}
	if transaction.Deleted {
		return false, nil
	}
	if transaction.Type != models.TRANSACTION_DB_TYPE_INCOME && transaction.Type != models.TRANSACTION_DB_TYPE_EXPENSE {
		return false, fmt.Errorf("反入账仅能撤销由账单创建的收入或支出交易")
	}
	var account models.Account
	if ok, err = sess.Where("uid = ? AND account_id = ?", uid, transaction.AccountId).Get(&account); err != nil || !ok {
		if err != nil {
			return false, err
		}
		return false, errors.New("入账交易对应的主账本账户不存在")
	}
	if affected, updateErr := sess.ID(transaction.TransactionId).Where("uid = ? AND deleted = ?", uid, false).Cols("deleted", "deleted_unix_time").Update(&models.Transaction{Deleted: true, DeletedUnixTime: now}); updateErr != nil {
		return false, updateErr
	} else if affected != 1 {
		return false, errors.New("入账交易未能完整撤销")
	}
	if _, err = sess.Where("uid = ? AND transaction_id = ? AND deleted = ?", uid, transaction.TransactionId, false).Cols("deleted", "deleted_unix_time").Update(&models.TransactionTagIndex{Deleted: true, DeletedUnixTime: now}); err != nil {
		return false, err
	}
	if _, err = sess.Where("uid = ? AND transaction_id = ? AND deleted = ?", uid, transaction.TransactionId, false).Cols("deleted", "deleted_unix_time").Update(&models.TransactionPictureInfo{Deleted: true, DeletedUnixTime: now}); err != nil {
		return false, err
	}
	balanceDelta := transaction.Amount
	if transaction.Type == models.TRANSACTION_DB_TYPE_INCOME {
		balanceDelta = -transaction.Amount
	}
	if affected, updateErr := sess.ID(account.AccountId).Where("uid = ?", uid).SetExpr("balance", fmt.Sprintf("balance+(%d)", balanceDelta)).Cols("updated_unix_time").Update(&models.Account{UpdatedUnixTime: now}); updateErr != nil {
		return false, updateErr
	} else if affected != 1 {
		return false, errors.New("入账交易对应的账户余额未能回退")
	}
	return true, nil
}

func (a *HengcaiApi) unpostStatement(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		StatementId int64  `json:"statement_id,string"`
		Month       string `json:"month"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.StatementId <= 0 || !validMonth(input.Month) {
		return nil, hcError(errors.New("statement_id 或 month 无效"))
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		input.Reason = "修改账单凭据"
	}
	if len([]rune(input.Reason)) > 255 {
		return nil, hcError(errors.New("反入账原因不能超过 255 个字"))
	}
	uid := c.GetCurrentUid()
	var statement hengcai.StatementImport
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, input.StatementId).Get(&statement); err != nil || !ok {
		return nil, hcError(errors.New("账单不存在"))
	}
	if !statementOverlapsMonth(&statement, input.Month) {
		return nil, hcError(errors.New("账单期间与当前对账月份不一致"))
	}
	if statement.Status != "POSTED" {
		return nil, hcError(errors.New("只有已入账账单可以反入账"))
	}
	postingMonth := statementMonth(&statement)
	if postingMonth == "" {
		postingMonth = input.Month
	}
	firstCoveredMonth, lastCoveredMonth := postingMonth, postingMonth
	if len(statement.PeriodStart) >= 7 {
		firstCoveredMonth = statement.PeriodStart[:7]
	}
	if len(statement.PeriodEnd) >= 7 {
		lastCoveredMonth = statement.PeriodEnd[:7]
	}
	var closedMonths []hengcai.MonthClose
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND status = ? AND month >= ? AND month <= ?", uid, "CLOSED", firstCoveredMonth, lastCoveredMonth).Asc("month").Find(&closedMonths); err != nil {
		return nil, errs.ErrOperationFailed
	} else if len(closedMonths) > 0 {
		return nil, hcError(fmt.Errorf("账单覆盖的 %s 已完成月结，不能反入账；如需调整请先重新打开该月", closedMonths[0].Month))
	}
	var lines []*hengcai.StatementLine
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND statement_id = ?", uid, input.StatementId).Find(&lines); err != nil {
		return nil, errs.ErrOperationFailed
	}

	deletedTransactions, restoredEvidence, revertedCapex := 0, 0, 0
	now := time.Now().Unix()
	err := datastore.Container.UserDataStore.DoTransaction(uid, c, func(sess *xorm.Session) error {
		origins := make(map[int64]*hengcai.TransactionOrigin)
		for _, line := range lines {
			var origin hengcai.TransactionOrigin
			if ok, queryErr := sess.Where("uid = ? AND statement_line_id = ?", uid, line.Id).Get(&origin); queryErr != nil {
				return queryErr
			} else if ok {
				origins[line.Id] = &origin
				var linkedEvidence []*hengcai.TransactionEvidence
				if queryErr = sess.Where("uid = ? AND transaction_id = ?", uid, origin.TransactionId).Find(&linkedEvidence); queryErr != nil {
					return queryErr
				}
				for _, evidence := range linkedEvidence {
					if evidence.StatementLineId != line.Id {
						return fmt.Errorf("流水 %d 创建的交易已被其他账单凭据引用，请先处理关联账单", line.LineNumber)
					}
				}
			}
		}

		for _, line := range lines {
			if line.Status != "POSTED" {
				continue
			}
			if line.LineKind == statementparser.LineKindInstallmentPrincipal {
				var settlement hengcai.CapexInstallmentSettlement
				if ok, queryErr := sess.Where("uid = ? AND statement_line_id = ?", uid, line.Id).Get(&settlement); queryErr != nil {
					return queryErr
				} else if ok && settlement.Posted {
					var installment hengcai.CapexInstallment
					if ok, queryErr = sess.Where("uid = ? AND id = ?", uid, settlement.InstallmentId).Get(&installment); queryErr != nil || !ok {
						if queryErr != nil {
							return queryErr
						}
						return errors.New("反入账的 CAPEX 期次不存在")
					}
					actualPaid := installment.ActualPaidMinor - settlement.PrincipalMinor
					if actualPaid < 0 {
						actualPaid = 0
					}
					status := "SCHEDULED"
					if actualPaid >= installment.PrincipalMinor {
						status = "PAID"
					} else if actualPaid > 0 {
						status = "PARTIALLY_PAID"
					}
					if _, queryErr = sess.ID(installment.Id).Where("uid = ?", uid).Cols("actual_paid_minor", "status").Update(&hengcai.CapexInstallment{ActualPaidMinor: actualPaid, Status: status}); queryErr != nil {
						return queryErr
					}
					if _, queryErr = sess.ID(settlement.Id).Where("uid = ?", uid).Cols("posted", "updated_unix_time").Update(&hengcai.CapexInstallmentSettlement{Posted: false, UpdatedUnixTime: now}); queryErr != nil {
						return queryErr
					}
					revertedCapex++
				}
			}

			origin, created := origins[line.Id]
			if created {
				deleted, deleteErr := reverseStatementCreatedTransaction(sess, uid, origin.TransactionId, now)
				if deleteErr != nil {
					return deleteErr
				}
				if deleted {
					deletedTransactions++
				}
				if _, deleteErr = sess.Where("uid = ? AND statement_line_id = ?", uid, line.Id).Delete(&hengcai.TransactionOrigin{}); deleteErr != nil {
					return deleteErr
				}
			}
			if affected, deleteErr := sess.Where("uid = ? AND statement_line_id = ?", uid, line.Id).Delete(&hengcai.TransactionEvidence{}); deleteErr != nil {
				return deleteErr
			} else if affected > 0 {
				restoredEvidence++
			}
			status, matchedTransactionID := restoredStatementLineState(line, created)
			if _, updateErr := sess.ID(line.Id).Where("uid = ?", uid).Cols("status", "matched_transaction_id").Update(&hengcai.StatementLine{Status: status, MatchedTransactionId: matchedTransactionID}); updateErr != nil {
				return updateErr
			}
		}
		if _, updateErr := sess.ID(statement.Id).Where("uid = ?", uid).Cols("status", "coverage_status", "covered_until").Update(&hengcai.StatementImport{Status: "REVIEW", CoverageStatus: "PENDING", CoveredUntil: ""}); updateErr != nil {
			return updateErr
		}
		if _, updateErr := sess.Where("uid = ? AND statement_id = ?", uid, statement.Id).Cols("status", "covered_until", "updated_unix_time").Update(&hengcai.TransactionCoverage{Status: "PENDING", CoveredUntil: "", UpdatedUnixTime: now}); updateErr != nil {
			return updateErr
		}
		_, insertErr := sess.Insert(&hengcai.StatementPostingReversal{Uid: uid, StatementId: statement.Id, Month: postingMonth, Reason: input.Reason, DeletedTransactionCount: deletedTransactions, RestoredEvidenceCount: restoredEvidence, RevertedCapexCount: revertedCapex, CreatedUnixTime: now})
		return insertErr
	})
	if err != nil {
		log.Errorf(c, "[hengcai.unpostStatement] failed to reverse statement %d: %s", input.StatementId, err.Error())
		return nil, hcError(fmt.Errorf("账单反入账失败：%s", err.Error()))
	}
	return map[string]any{"statement_id": input.StatementId, "month": postingMonth, "view_month": input.Month, "status": "REVIEW", "deleted_transactions": deletedTransactions, "restored_evidence": restoredEvidence, "reverted_capex": revertedCapex}, nil
}

func (a *HengcaiApi) closeMonth(c *core.WebContext) (any, *errs.Error) {
	var input struct{ Month, Note string }
	if err := c.ShouldBindJSON(&input); err != nil || !validMonth(input.Month) {
		return nil, hcError(errors.New("month 必须为 YYYY-MM"))
	}
	uid := c.GetCurrentUid()
	close := &hengcai.MonthClose{Uid: uid, Month: input.Month, Status: "CLOSED", ClosedUnixTime: time.Now().Unix(), Note: input.Note}
	var statements []*hengcai.StatementImport
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND (period_type IS NULL OR period_type = '' OR period_type = ?) AND period_end LIKE ?", uid, "CALENDAR_MONTH", input.Month+"%").Find(&statements)
	close.StatementCount = len(statements)
	parsedMonth, _ := time.Parse("2006-01", input.Month)
	monthStart := parsedMonth.Format("2006-01-02")
	monthEnd := time.Date(parsedMonth.Year(), parsedMonth.Month()+1, 0, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	var overlappingStatements []*hengcai.StatementImport
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND period_start <= ? AND period_end >= ?", uid, monthEnd, monthStart).Find(&overlappingStatements)
	for _, statement := range overlappingStatements {
		if statement.Status != "POSTED" {
			name := statement.FileName
			if name == "" {
				name = statement.DisplayName
			}
			return nil, hcError(fmt.Errorf("账单“%s”尚未入账，不能月结", name))
		}
	}
	var lines []*hengcai.StatementLine
	_ = datastore.Container.UserDataStore.Query(c, uid).Table("hengcai_statement_line").Join("INNER", "hengcai_statement_import", "hengcai_statement_line.statement_id = hengcai_statement_import.id").Where("hengcai_statement_line.uid = ? AND (hengcai_statement_import.period_type IS NULL OR hengcai_statement_import.period_type = '' OR hengcai_statement_import.period_type = ?) AND hengcai_statement_import.period_end LIKE ? AND hengcai_statement_line.status IN (?, ?)", uid, "CALENDAR_MONTH", input.Month+"%", "UNMATCHED", "REVIEW").Find(&lines)
	close.UnmatchedLineCount = len(lines)
	if close.UnmatchedLineCount > 0 {
		return nil, hcError(fmt.Errorf("本月仍有 %d 笔待匹配或待审核流水，不能月结", close.UnmatchedLineCount))
	}
	var origins []*hengcai.TransactionOrigin
	_ = datastore.Container.UserDataStore.Query(c, uid).Table("hengcai_transaction_origin").Join("INNER", "hengcai_statement_line", "hengcai_transaction_origin.statement_line_id = hengcai_statement_line.id").Join("INNER", "hengcai_statement_import", "hengcai_statement_line.statement_id = hengcai_statement_import.id").Where("hengcai_transaction_origin.uid = ? AND hengcai_statement_import.period_end LIKE ?", uid, input.Month+"%").Find(&origins)
	close.ConfirmedTransactionCount = len(origins)
	if _, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ?", uid, input.Month).Cols("status", "statement_count", "unmatched_line_count", "closed_unix_time", "note").Update(close); err != nil {
		if _, err := datastore.Container.UserDataStore.Query(c, uid).Insert(close); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}
	return close, nil
}

func validMonth(v string) bool { _, err := time.Parse("2006-01", v); return err == nil && len(v) == 7 }

func addMonthsToDate(value string, months int) (string, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", err
	}
	firstOfTarget := time.Date(parsed.Year(), parsed.Month()+time.Month(months), 1, 0, 0, 0, 0, parsed.Location())
	lastDay := firstOfTarget.AddDate(0, 1, -1).Day()
	day := parsed.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(firstOfTarget.Year(), firstOfTarget.Month(), day, 0, 0, 0, 0, parsed.Location()).Format("2006-01-02"), nil
}

func splitAmount(total int64, count, index int) int64 {
	if total <= 0 || count <= 0 || index < 0 || index >= count {
		return 0
	}
	base := total / int64(count)
	if index < int(total%int64(count)) {
		return base + 1
	}
	return base
}

func alpacaCipher(secret string) (cipher.AEAD, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealAlpacaSecret(key, plaintext string) (string, error) {
	aead, err := alpacaCipher(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), nil)
	encoded := append(nonce, ciphertext...)
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(encoded), nil
}

func openAlpacaSecret(key, sealed string) (string, error) {
	aead, err := alpacaCipher(key)
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, "enc:v1:"))
	if err != nil {
		return "", err
	}
	if len(data) < aead.NonceSize() {
		return "", errors.New("invalid encrypted secret")
	}
	plaintext, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (a *HengcaiApi) listInvestments(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	accounts := make([]*hengcai.InvestmentAccount, 0)
	instruments := make([]*hengcai.InvestmentInstrument, 0)
	txs := make([]*hengcai.InvestmentTransaction, 0)
	if err := a.rebuildInvestmentPositions(c, uid); err != nil {
		return nil, errs.ErrOperationFailed
	}
	positions := make([]*hengcai.InvestmentPosition, 0)
	valuations := make([]*hengcai.InvestmentAccountValuation, 0)
	returns := make([]*hengcai.InvestmentReturn, 0)
	sess := datastore.Container.UserDataStore.Query(c, uid)
	if err := sess.Where("uid = ?", uid).Find(&accounts); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&instruments); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Desc("traded_at").Find(&txs); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Desc("as_of_unix_time").Find(&positions); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&valuations); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Desc("month").Find(&returns); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"accounts": accounts, "instruments": instruments, "transactions": txs, "positions": positions, "valuations": valuations, "returns": returns}, nil
}

func (a *HengcaiApi) rebuildInvestmentPositions(c *core.WebContext, uid int64) error {
	var txs []*hengcai.InvestmentTransaction
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Asc("traded_at").Asc("id").Find(&txs); err != nil {
		return err
	}
	var instruments []*hengcai.InvestmentInstrument
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&instruments); err != nil {
		return err
	}
	instrumentByID := make(map[int64]*hengcai.InvestmentInstrument, len(instruments))
	for _, instrument := range instruments {
		instrumentByID[instrument.Id] = instrument
	}
	type aggregate struct {
		account, instrument int64
		quantity, cost      float64
		currency            string
	}
	aggregates := make(map[string]*aggregate)
	month := time.Now().Format("2006-01")
	var realizedPnlMinor int64
	for _, tx := range txs {
		key := fmt.Sprintf("%d/%d", tx.InvestmentAccountId, tx.InstrumentId)
		agg := aggregates[key]
		if agg == nil {
			agg = &aggregate{account: tx.InvestmentAccountId, instrument: tx.InstrumentId, currency: tx.Currency}
			aggregates[key] = agg
		}
		realized := applyInvestmentPositionTransaction(&agg.quantity, &agg.cost, tx)
		if time.Unix(tx.TradedAt, 0).Format("2006-01") == month {
			realizedPnlMinor += int64(math.Round(realized))
		}
	}
	now := time.Now().Unix()
	activePositionKeys := make(map[string]bool)
	var totalUnrealizedPnlMinor int64
	var totalCostValueMinor int64
	allPositionsMarked := true
	for key, agg := range aggregates {
		if math.Abs(agg.quantity) <= 1e-8 {
			continue
		}
		activePositionKeys[key] = true
		var price hengcai.MarketPrice
		hasPrice, _ := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND instrument_id = ?", uid, agg.instrument).Desc("as_of_unix_time").Get(&price)
		costValue := int64(math.Round(agg.cost))
		marketValue := costValue
		pnl := int64(0)
		if hasPrice && price.Close > 0 {
			multiplier := 1
			if instrument := instrumentByID[agg.instrument]; instrument != nil && instrument.ContractMultiplier > 0 {
				multiplier = instrument.ContractMultiplier
			}
			marketValue = int64(math.Round(agg.quantity * price.Close * float64(multiplier) * 100))
			pnl = marketValue - costValue
		} else {
			allPositionsMarked = false
		}
		returnBps := 0
		if costValue != 0 {
			returnBps = int(math.Round(float64(pnl) * 10000 / math.Abs(float64(costValue))))
		}
		position := &hengcai.InvestmentPosition{Uid: uid, InvestmentAccountId: agg.account, InstrumentId: agg.instrument, Quantity: agg.quantity, AverageCost: math.Abs(agg.cost / agg.quantity), MarketPrice: price.Close, MarketValueMinor: marketValue, CostValueMinor: costValue, UnrealizedPnlMinor: pnl, ReturnBps: returnBps, AsOfUnixTime: now}
		updated, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND investment_account_id = ? AND instrument_id = ?", uid, agg.account, agg.instrument).Cols("quantity", "average_cost", "market_price", "market_value_minor", "cost_value_minor", "unrealized_pnl_minor", "return_bps", "as_of_unix_time").Update(position)
		if err != nil {
			return err
		}
		if updated == 0 {
			if _, err := datastore.Container.UserDataStore.Query(c, uid).Insert(position); err != nil {
				return err
			}
		}
		totalUnrealizedPnlMinor += pnl
		totalCostValueMinor += int64(math.Abs(float64(costValue)))
	}
	var existingPositions []*hengcai.InvestmentPosition
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&existingPositions); err != nil {
		return err
	}
	for _, position := range existingPositions {
		key := fmt.Sprintf("%d/%d", position.InvestmentAccountId, position.InstrumentId)
		if !activePositionKeys[key] {
			if _, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, position.Id).Delete(&hengcai.InvestmentPosition{}); err != nil {
				return err
			}
		}
	}
	totalReturnMinor := realizedPnlMinor + totalUnrealizedPnlMinor
	returnBps := 0
	if totalCostValueMinor > 0 {
		returnBps = int(math.Round(float64(totalReturnMinor) * 10000 / float64(totalCostValueMinor)))
	}
	quality := "COST_ONLY"
	if len(activePositionKeys) > 0 && allPositionsMarked {
		quality = "MARKED"
	}
	ret := &hengcai.InvestmentReturn{Uid: uid, Month: month, RealizedPnlMinor: realizedPnlMinor, UnrealizedPnlMinor: totalUnrealizedPnlMinor, TotalReturnMinor: totalReturnMinor, ReturnBps: returnBps, Quality: quality, UpdatedUnixTime: now}
	updated, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ?", uid, month).Cols("realized_pnl_minor", "unrealized_pnl_minor", "total_return_minor", "return_bps", "quality", "updated_unix_time").Update(ret)
	if err != nil {
		return err
	}
	if updated == 0 {
		_, err = datastore.Container.UserDataStore.Query(c, uid).Insert(ret)
	}
	if err != nil {
		return err
	}
	return a.refreshInvestmentValuations(c, uid)
}

// applyInvestmentPositionTransaction keeps the signed position quantity. This is
// essential for short options: SELL-to-open followed by BUY-to-close must net to
// zero instead of being mistaken for a new long position.
func applyInvestmentPositionTransaction(quantity, carryingCost *float64, tx *hengcai.InvestmentTransaction) float64 {
	delta := tx.QuantityDelta
	if math.Abs(delta) <= 1e-8 {
		return 0
	}
	buyCost := math.Abs(float64(tx.GrossAmountMinor + tx.FeesMinor + tx.TaxesMinor))
	sellProceeds := float64(tx.GrossAmountMinor - tx.FeesMinor - tx.TaxesMinor)
	if sellProceeds < 0 {
		sellProceeds = 0
	}

	// Empty position or adding in the same direction.
	if math.Abs(*quantity) <= 1e-8 || (*quantity > 0 && delta > 0) || (*quantity < 0 && delta < 0) {
		if delta > 0 {
			*carryingCost += buyCost
		} else {
			*carryingCost -= sellProceeds
		}
		*quantity += delta
		return 0
	}

	tradeQuantity := math.Abs(delta)
	closedQuantity := math.Min(math.Abs(*quantity), tradeQuantity)
	closedRatio := closedQuantity / tradeQuantity
	realized := float64(0)
	if *quantity > 0 {
		basis := math.Abs(*carryingCost / *quantity) * closedQuantity
		realized = sellProceeds*closedRatio - basis
		*carryingCost -= basis
	} else {
		shortProceeds := math.Abs(*carryingCost / *quantity) * closedQuantity
		realized = shortProceeds - buyCost*closedRatio
		*carryingCost += shortProceeds
	}

	*quantity += delta
	remainingRatio := 1 - closedRatio
	if remainingRatio > 1e-8 {
		if delta > 0 {
			*carryingCost += buyCost * remainingRatio
		} else {
			*carryingCost -= sellProceeds * remainingRatio
		}
	}
	if math.Abs(*quantity) <= 1e-8 {
		*quantity = 0
		*carryingCost = 0
	}
	return realized
}

func (a *HengcaiApi) createInvestment(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		Kind        string                         `json:"kind"`
		Account     *hengcai.InvestmentAccount     `json:"account"`
		Instrument  *hengcai.InvestmentInstrument  `json:"instrument"`
		Transaction *hengcai.InvestmentTransaction `json:"transaction"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		return nil, hcError(err)
	}
	uid := c.GetCurrentUid()
	sess := datastore.Container.UserDataStore.Query(c, uid)
	switch strings.ToLower(input.Kind) {
	case "account":
		if input.Account == nil || strings.TrimSpace(input.Account.Name) == "" {
			return nil, hcError(errors.New("投资账户名称不能为空"))
		}
		input.Account.Name = strings.TrimSpace(input.Account.Name)
		input.Account.Institution = strings.TrimSpace(input.Account.Institution)
		input.Account.Uid = uid
		input.Account.Active = true
		input.Account.BaseCurrency = strings.ToUpper(strings.TrimSpace(input.Account.BaseCurrency))
		if input.Account.BaseCurrency == "" {
			input.Account.BaseCurrency = "USD"
		}
		input.Account.AccountType = strings.ToUpper(strings.TrimSpace(input.Account.AccountType))
		if input.Account.AccountType == "" {
			input.Account.AccountType = "BROKERAGE"
		}
		input.Account.CreatedUnixTime = time.Now().Unix()
		err := datastore.Container.UserDataStore.DoTransaction(uid, c, func(txSession *xorm.Session) error {
			var duplicate hengcai.InvestmentAccount
			if exists, err := txSession.Where("uid = ? AND active = ? AND LOWER(name) = ? AND LOWER(institution) = ?", uid, true, strings.ToLower(input.Account.Name), strings.ToLower(input.Account.Institution)).Get(&duplicate); err != nil {
				return err
			} else if exists {
				return errors.New("同名且同机构的投资账户已存在")
			}
			if input.Account.AccountId > 0 {
				if _, err := validateCoreInvestmentSummaryAccount(txSession, uid, input.Account.AccountId, input.Account.BaseCurrency); err != nil {
					return err
				}
				if err := ensureCoreInvestmentAccountNotLinked(txSession, uid, 0, input.Account.AccountId); err != nil {
					return err
				}
			} else {
				coreAccount, err := createCoreInvestmentSummaryAccount(txSession, uid, input.Account)
				if err != nil {
					return err
				}
				input.Account.AccountId = coreAccount.AccountId
			}
			_, err := txSession.Insert(input.Account)
			return err
		})
		if err != nil {
			return nil, hcError(err)
		}
		if err := a.refreshInvestmentValuations(c, uid); err != nil {
			return nil, errs.ErrOperationFailed
		}
		return input.Account, nil
	case "instrument":
		if input.Instrument == nil || strings.TrimSpace(input.Instrument.Symbol) == "" || strings.TrimSpace(input.Instrument.Name) == "" {
			return nil, hcError(errors.New("投资标的代码和名称不能为空"))
		}
		input.Instrument.Name = strings.TrimSpace(input.Instrument.Name)
		input.Instrument.Uid = uid
		input.Instrument.Active = true
		input.Instrument.Symbol = strings.ToUpper(strings.TrimSpace(input.Instrument.Symbol))
		input.Instrument.Currency = strings.ToUpper(strings.TrimSpace(input.Instrument.Currency))
		if input.Instrument.Currency == "" {
			input.Instrument.Currency = "USD"
		}
		input.Instrument.AssetType = strings.ToUpper(strings.TrimSpace(input.Instrument.AssetType))
		if input.Instrument.AssetType == "" {
			input.Instrument.AssetType = "STOCK"
		}
		input.Instrument.Market = strings.ToUpper(strings.TrimSpace(input.Instrument.Market))
		if input.Instrument.Market == "" {
			input.Instrument.Market = "US"
		}
		if input.Instrument.PriceScale <= 0 {
			input.Instrument.PriceScale = 4
		}
		if input.Instrument.QuantityScale <= 0 {
			input.Instrument.QuantityScale = 6
		}
		if input.Instrument.ContractKey == "" {
			input.Instrument.ContractKey = input.Instrument.Symbol
		}
		input.Instrument.UnderlyingSymbol = strings.ToUpper(strings.TrimSpace(input.Instrument.UnderlyingSymbol))
		input.Instrument.OptionType = strings.ToUpper(strings.TrimSpace(input.Instrument.OptionType))
		if input.Instrument.ContractMultiplier <= 0 {
			input.Instrument.ContractMultiplier = 1
			if input.Instrument.AssetType == "OPTION" {
				input.Instrument.ContractMultiplier = 100
			}
		}
		var duplicate hengcai.InvestmentInstrument
		if exists, err := sess.Where("uid = ? AND active = ? AND market = ? AND symbol = ?", uid, true, input.Instrument.Market, input.Instrument.Symbol).Get(&duplicate); err != nil {
			return nil, errs.ErrOperationFailed
		} else if exists {
			return nil, hcError(fmt.Errorf("投资标的 %s:%s 已存在", input.Instrument.Market, input.Instrument.Symbol))
		}
		if _, err := sess.Insert(input.Instrument); err != nil {
			return nil, errs.ErrOperationFailed
		}
		return input.Instrument, nil
	case "transaction":
		if input.Transaction == nil || input.Transaction.InvestmentAccountId <= 0 || input.Transaction.InstrumentId <= 0 {
			return nil, hcError(errors.New("投资交易必须选择账户和标的"))
		}
		if input.Transaction.Quantity <= 0 || input.Transaction.Price <= 0 || input.Transaction.FeesMinor < 0 || input.Transaction.TaxesMinor < 0 {
			return nil, hcError(errors.New("投资交易数量和价格无效"))
		}
		var investmentAccount hengcai.InvestmentAccount
		if exists, err := sess.Where("uid = ? AND id = ? AND active = ?", uid, input.Transaction.InvestmentAccountId, true).Get(&investmentAccount); err != nil {
			return nil, errs.ErrOperationFailed
		} else if !exists {
			return nil, hcError(errors.New("投资账户不存在、已停用或不属于当前用户"))
		}
		var instrument hengcai.InvestmentInstrument
		if exists, err := sess.Where("uid = ? AND id = ? AND active = ?", uid, input.Transaction.InstrumentId, true).Get(&instrument); err != nil {
			return nil, errs.ErrOperationFailed
		} else if !exists {
			return nil, hcError(errors.New("投资标的不存在、已停用或不属于当前用户"))
		}
		input.Transaction.Uid = uid
		input.Transaction.Action = strings.ToUpper(strings.TrimSpace(input.Transaction.Action))
		if input.Transaction.Action == "" {
			input.Transaction.Action = "BUY"
		}
		if input.Transaction.Action != "BUY" && input.Transaction.Action != "SELL" {
			return nil, hcError(errors.New("交易动作只能是 BUY 或 SELL"))
		}
		if input.Transaction.Action == "SELL" {
			var existingTransactions []*hengcai.InvestmentTransaction
			if err := sess.Where("uid = ? AND investment_account_id = ? AND instrument_id = ?", uid, input.Transaction.InvestmentAccountId, input.Transaction.InstrumentId).Find(&existingTransactions); err != nil {
				return nil, errs.ErrOperationFailed
			}
			var availableQuantity float64
			for _, existing := range existingTransactions {
				availableQuantity += existing.QuantityDelta
			}
			if input.Transaction.Quantity > availableQuantity+1e-9 {
				return nil, hcError(fmt.Errorf("卖出数量 %.6f 超过可用持仓 %.6f", input.Transaction.Quantity, availableQuantity))
			}
			input.Transaction.QuantityDelta = -math.Abs(input.Transaction.Quantity)
		} else {
			input.Transaction.QuantityDelta = math.Abs(input.Transaction.Quantity)
		}
		input.Transaction.GrossAmountMinor = int64(math.Round(input.Transaction.Quantity * input.Transaction.Price * 100))
		input.Transaction.Currency = instrument.Currency
		if input.Transaction.Action == "BUY" {
			input.Transaction.NetCashAmountMinor = -(input.Transaction.GrossAmountMinor + input.Transaction.FeesMinor + input.Transaction.TaxesMinor)
		} else {
			input.Transaction.NetCashAmountMinor = input.Transaction.GrossAmountMinor - input.Transaction.FeesMinor - input.Transaction.TaxesMinor
		}
		if input.Transaction.TradedAt == 0 {
			input.Transaction.TradedAt = time.Now().Unix()
		}
		if input.Transaction.Source == "" {
			input.Transaction.Source = "MANUAL"
		}
		if _, err := sess.Insert(input.Transaction); err != nil {
			return nil, errs.ErrOperationFailed
		}
		if err := a.rebuildInvestmentPositions(c, uid); err != nil {
			return nil, errs.ErrOperationFailed
		}
		return input.Transaction, nil
	default:
		return nil, hcError(errors.New("kind 必须是 account、instrument 或 transaction"))
	}
}

func (a *HengcaiApi) deleteInvestment(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		Kind string `json:"kind"`
		Id   int64  `json:"id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		return nil, hcError(err)
	}
	if input.Id <= 0 {
		return nil, hcError(errors.New("待删除记录的 ID 无效"))
	}

	uid := c.GetCurrentUid()
	var deletedName string
	err := datastore.Container.UserDataStore.DoTransaction(uid, c, func(sess *xorm.Session) error {
		switch strings.ToLower(strings.TrimSpace(input.Kind)) {
		case "account":
			var account hengcai.InvestmentAccount
			exists, err := sess.Where("uid = ? AND id = ?", uid, input.Id).Get(&account)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("投资账户不存在或已删除")
			}
			count, err := sess.Where("uid = ? AND investment_account_id = ?", uid, input.Id).Count(&hengcai.InvestmentTransaction{})
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("投资账户“%s”已有 %d 条交易，请先处理关联交易", account.Name, count)
			}
			if _, err := sess.Where("uid = ? AND investment_account_id = ?", uid, input.Id).Delete(&hengcai.InvestmentPosition{}); err != nil {
				return err
			}
			if _, err := sess.Where("uid = ? AND investment_account_id = ?", uid, input.Id).Delete(&hengcai.InvestmentAccountValuation{}); err != nil {
				return err
			}
			deleted, err := sess.Where("uid = ? AND id = ?", uid, input.Id).Delete(&hengcai.InvestmentAccount{})
			if err != nil {
				return err
			}
			if deleted != 1 {
				return errors.New("投资账户删除失败")
			}
			deletedName = account.Name
		case "instrument":
			var instrument hengcai.InvestmentInstrument
			exists, err := sess.Where("uid = ? AND id = ?", uid, input.Id).Get(&instrument)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("投资标的不存在或已删除")
			}
			count, err := sess.Where("uid = ? AND instrument_id = ?", uid, input.Id).Count(&hengcai.InvestmentTransaction{})
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("投资标的“%s”已有 %d 条交易，请先处理关联交易", instrument.Symbol, count)
			}
			if _, err := sess.Where("uid = ? AND instrument_id = ?", uid, input.Id).Delete(&hengcai.InvestmentPosition{}); err != nil {
				return err
			}
			if _, err := sess.Where("uid = ? AND instrument_id = ?", uid, input.Id).Delete(&hengcai.MarketPrice{}); err != nil {
				return err
			}
			deleted, err := sess.Where("uid = ? AND id = ?", uid, input.Id).Delete(&hengcai.InvestmentInstrument{})
			if err != nil {
				return err
			}
			if deleted != 1 {
				return errors.New("投资标的删除失败")
			}
			deletedName = instrument.Symbol
		case "transaction":
			var transaction hengcai.InvestmentTransaction
			exists, err := sess.Where("uid = ? AND id = ?", uid, input.Id).Get(&transaction)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("投资交易不存在或已删除")
			}
			deleted, err := sess.Where("uid = ? AND id = ?", uid, input.Id).Delete(&hengcai.InvestmentTransaction{})
			if err != nil {
				return err
			}
			if deleted != 1 {
				return errors.New("投资交易删除失败")
			}
			deletedName = fmt.Sprintf("%s %.6f", transaction.Action, transaction.Quantity)
		default:
			return errors.New("删除类型只能是 account、instrument 或 transaction")
		}
		return nil
	})
	if err != nil {
		return nil, hcError(err)
	}
	if err := a.rebuildInvestmentPositions(c, uid); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"kind": strings.ToLower(input.Kind), "id": input.Id, "name": deletedName}, nil
}

func (a *HengcaiApi) syncPrices(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	var setting hengcai.AlpacaSetting
	ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Get(&setting)
	if err != nil || !ok {
		return nil, hcError(errors.New("请先在衡财基础设置中保存 Alpaca 设置"))
	}
	mode := strings.ToLower(strings.TrimSpace(c.Query("mode")))
	manualLatest := mode == "" || mode == "latest"
	dayStart := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	if !manualLatest && setting.LastSyncUnixTime >= dayStart {
		return map[string]any{"provider": "ALPACA", "prices": []*hengcai.MarketPrice{}, "errors": []string{}, "skipped": true, "message": "今天已查询过收盘价，下一次查询将在明天"}, nil
	}
	secret := setting.SecretKey
	if strings.HasPrefix(secret, "enc:v1:") {
		secret, err = openAlpacaSecret(a.CurrentConfig().SecretKey, secret)
		if err != nil {
			return nil, hcError(errors.New("Alpaca 密钥无法解密，请重新保存"))
		}
	}
	client, err := alpaca.NewClient(alpaca.Config{APIKeyID: setting.ApiKeyId, APISecretKey: secret, DataBaseURL: setting.DataUrl}, nil)
	if err != nil {
		return nil, hcError(err)
	}
	feed := strings.TrimSpace(c.Query("feed"))
	if feed == "" {
		feed = "iex"
	}
	if err := a.rebuildInvestmentPositions(c, uid); err != nil {
		return nil, errs.ErrOperationFailed
	}
	var positions []*hengcai.InvestmentPosition
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&positions); err != nil {
		return nil, errs.ErrOperationFailed
	}
	heldInstrumentIDs := make(map[int64]bool, len(positions))
	for _, position := range positions {
		heldInstrumentIDs[position.InstrumentId] = true
	}
	var instruments []*hengcai.InvestmentInstrument
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND active = ?", uid, true).Find(&instruments); err != nil {
		return nil, errs.ErrOperationFailed
	}
	prices := make([]*hengcai.MarketPrice, 0, len(instruments))
	errorsBySymbol := make([]string, 0)
	for _, instrument := range instruments {
		if !heldInstrumentIDs[instrument.Id] {
			continue
		}
		assetType := strings.ToUpper(strings.TrimSpace(instrument.AssetType))
		market := strings.ToUpper(strings.TrimSpace(instrument.Market))
		if market != "US" || (assetType != "STOCK" && assetType != "ETF") {
			errorsBySymbol = append(errorsBySymbol, fmt.Sprintf("%s: Alpaca 当前仅同步美股和美股 ETF", instrument.Symbol))
			continue
		}
		var bar alpaca.LatestBar
		var fetchErr error
		if manualLatest {
			bar, fetchErr = client.LatestStockBar(context.Background(), instrument.Symbol, feed)
		} else {
			bar, fetchErr = client.LatestDailyBar(context.Background(), instrument.Symbol, feed)
		}
		if fetchErr != nil {
			errorsBySymbol = append(errorsBySymbol, fmt.Sprintf("%s: %s", instrument.Symbol, fetchErr.Error()))
			continue
		}
		price := &hengcai.MarketPrice{Uid: uid, InstrumentId: instrument.Id, AsOfUnixTime: bar.Timestamp.Unix(), Provider: "ALPACA", Feed: feed, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, RawPayload: string(bar.Raw)}
		var existing hengcai.MarketPrice
		updated, updateErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND instrument_id = ? AND as_of_unix_time = ?", uid, price.InstrumentId, price.AsOfUnixTime).Cols("provider", "feed", "open", "high", "low", "close", "volume", "raw_payload").Update(price)
		if updateErr != nil {
			return nil, errs.ErrOperationFailed
		}
		if updated == 0 {
			if _, insertErr := datastore.Container.UserDataStore.Query(c, uid).Insert(price); insertErr != nil {
				if _, getErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND instrument_id = ?", uid, price.InstrumentId).Desc("as_of_unix_time").Get(&existing); getErr == nil && existing.Id > 0 {
					price = &existing
				} else {
					return nil, errs.ErrOperationFailed
				}
			}
		}
		prices = append(prices, price)
	}
	if err := a.rebuildInvestmentPositions(c, uid); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if !manualLatest {
		setting.LastSyncUnixTime = time.Now().Unix()
		if _, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Cols("last_sync_unix_time").Update(&setting); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}
	message := "已拉取当前持仓的最新可用价格"
	syncMode := "LATEST"
	if !manualLatest {
		message = "已拉取最近一个完整交易日的收盘价"
		syncMode = "DAILY_CLOSE"
	}
	return map[string]any{"provider": "ALPACA", "mode": syncMode, "feed": feed, "prices": prices, "errors": errorsBySymbol, "skipped": false, "message": message}, nil
}

func (a *HengcaiApi) saveAlpaca(c *core.WebContext) (any, *errs.Error) {
	var input hengcai.AlpacaSetting
	if err := c.ShouldBindJSON(&input); err != nil {
		return nil, hcError(err)
	}
	input.Uid = c.GetCurrentUid()
	input.ApiKeyId = strings.TrimSpace(input.ApiKeyId)
	sess := datastore.Container.UserDataStore.Query(c, input.Uid)
	if input.SecretKey == "" {
		var existing hengcai.AlpacaSetting
		if ok, _ := sess.Where("uid = ?", input.Uid).Get(&existing); ok {
			input.SecretKey = existing.SecretKey
		}
	}
	if input.ApiKeyId == "" || input.SecretKey == "" {
		return nil, hcError(errors.New("Alpaca API Key ID 和 Secret Key 不能为空"))
	}
	if input.SecretKey != "" && !strings.HasPrefix(input.SecretKey, "enc:v1:") {
		sealed, sealErr := sealAlpacaSecret(a.CurrentConfig().SecretKey, input.SecretKey)
		if sealErr != nil {
			return nil, errs.ErrOperationFailed
		}
		input.SecretKey = sealed
	}
	input.Environment = strings.ToUpper(input.Environment)
	input.UpdatedUnixTime = time.Now().Unix()
	if input.TradingUrl == "" {
		input.TradingUrl = "https://paper-api.alpaca.markets"
	}
	if input.DataUrl == "" {
		input.DataUrl = "https://data.alpaca.markets"
	}
	updated, err := sess.Where("uid = ?", input.Uid).Cols("environment", "api_key_id", "secret_key", "trading_url", "data_url", "updated_unix_time").Update(&input)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	if updated == 0 {
		if _, err := sess.Insert(&input); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}
	input.SecretKey = ""
	return map[string]any{
		"configured":  true,
		"environment": input.Environment,
		"api_key_id":  input.ApiKeyId,
		"trading_url": input.TradingUrl,
		"data_url":    input.DataUrl,
	}, nil
}

func (a *HengcaiApi) getAlpaca(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	result := map[string]any{
		"configured":  false,
		"environment": "PAPER",
		"api_key_id":  "",
		"trading_url": "https://paper-api.alpaca.markets",
		"data_url":    "https://data.alpaca.markets",
	}
	var setting hengcai.AlpacaSetting
	ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Get(&setting)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	if !ok {
		return result, nil
	}
	result["configured"] = setting.ApiKeyId != "" && setting.SecretKey != ""
	result["environment"] = setting.Environment
	result["api_key_id"] = setting.ApiKeyId
	result["trading_url"] = setting.TradingUrl
	result["data_url"] = setting.DataUrl
	return result, nil
}

func (a *HengcaiApi) testAlpaca(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	var setting hengcai.AlpacaSetting
	ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Get(&setting)
	if err != nil || !ok {
		return nil, hcError(errors.New("请先保存 Alpaca 设置"))
	}
	if strings.TrimSpace(setting.ApiKeyId) == "" || strings.TrimSpace(setting.SecretKey) == "" {
		return nil, hcError(errors.New("Alpaca 密钥尚未完整保存，请重新填写 API Key ID 和 Secret Key 并保存"))
	}
	secret := setting.SecretKey
	if strings.HasPrefix(secret, "enc:v1:") {
		secret, err = openAlpacaSecret(a.CurrentConfig().SecretKey, secret)
		if err != nil {
			return nil, hcError(errors.New("Alpaca 密钥无法解密，请重新保存"))
		}
	}
	client, err := alpaca.NewClient(alpaca.Config{APIKeyID: setting.ApiKeyId, APISecretKey: secret, DataBaseURL: setting.DataUrl}, nil)
	if err != nil {
		return nil, hcError(err)
	}
	bar, err := client.LatestDailyBar(context.Background(), "SPY", "iex")
	if err != nil {
		return nil, hcError(err)
	}
	return map[string]any{"provider": "ALPACA", "mode": "MARKET_DATA_READ_ONLY", "symbol": bar.Symbol, "close": bar.Close, "timestamp": bar.Timestamp, "message": "行情接口连接成功（仅拉取已收盘日线，不执行交易）"}, nil
}

func (a *HengcaiApi) listPrices(c *core.WebContext) (any, *errs.Error) {
	rows := make([]*hengcai.MarketPrice, 0)
	if err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ?", c.GetCurrentUid()).Desc("as_of_unix_time").Limit(200).Find(&rows); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return rows, nil
}

func (a *HengcaiApi) capex(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	var purchases []*hengcai.CapexPurchase
	var installments []*hengcai.CapexInstallment
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Desc("id").Find(&purchases); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Desc("due_date").Find(&installments); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"purchases": purchases, "installments": installments}, nil
}

func (a *HengcaiApi) monthlyIncomeExpense(c *core.WebContext, uid int64, month string) (int64, int64, int, error) {
	parsed, err := time.Parse("2006-01", month)
	if err != nil {
		return 0, 0, 0, err
	}
	transactions, err := services.Transactions.GetTransactionsInMonthByPage(c, uid, int32(parsed.Year()), int32(parsed.Month()), 0, nil, nil, nil, false, "", "", core.MATCH_MODE_DEFAULT, false)
	if err != nil {
		return 0, 0, 0, err
	}
	income := int64(0)
	expense := int64(0)
	count := 0
	for _, transaction := range transactions {
		switch transaction.Type {
		case models.TRANSACTION_DB_TYPE_INCOME:
			income += transaction.Amount
			count++
		case models.TRANSACTION_DB_TYPE_EXPENSE:
			expense += transaction.Amount
			count++
		}
	}
	return income, expense, count, nil
}

func (a *HengcaiApi) trailingAverageIncomeExpense(c *core.WebContext, uid int64, anchor time.Time, months int) (int64, int64, error) {
	if months < 1 {
		months = 1
	}
	totalIncome := int64(0)
	totalExpense := int64(0)
	for i := 1; i <= months; i++ {
		month := anchor.AddDate(0, -i, 0).Format("2006-01")
		income, expense, _, err := a.monthlyIncomeExpense(c, uid, month)
		if err != nil {
			return 0, 0, err
		}
		totalIncome += income
		totalExpense += expense
	}
	return totalIncome / int64(months), totalExpense / int64(months), nil
}

func (a *HengcaiApi) monthlyCapexCommitment(c *core.WebContext, uid int64, month string, onlyUnpaid bool) (int64, error) {
	var installments []*hengcai.CapexInstallment
	sess := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND due_date LIKE ?", uid, month+"%")
	if onlyUnpaid {
		sess = sess.And("status <> ?", "PAID")
	}
	if err := sess.Find(&installments); err != nil {
		return 0, err
	}
	total := int64(0)
	for _, item := range installments {
		total += capexInstallmentPrincipalCashflow(item)
	}
	var purchases []*hengcai.CapexPurchase
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND purchase_date LIKE ?", uid, month+"%").Find(&purchases); err != nil {
		return 0, err
	}
	for _, purchase := range purchases {
		total += purchase.DownPaymentMinor
	}
	return total, nil
}

func capexInstallmentPrincipalCashflow(item *hengcai.CapexInstallment) int64 {
	if item == nil {
		return 0
	}
	// Interest and fees are ordinary OPEX transactions. CAPEX contains
	// principal only, otherwise cash-flow projections double count them.
	if item.Status == "PAID" && item.ActualPaidMinor > 0 && item.ActualPaidMinor < item.PrincipalMinor {
		return item.ActualPaidMinor
	}
	return item.PrincipalMinor
}

func forecastIncomeForMonth(setting *hengcai.IncomeForecastSetting, month string) (int64, error) {
	parsed, err := time.Parse("2006-01", month)
	if err != nil {
		return 0, err
	}
	if setting == nil {
		return 0, nil
	}
	income := setting.MonthlySalaryMinor + setting.MonthlyPerformanceMinor
	if int(parsed.Month())%3 == 0 {
		income += setting.QuarterlyPerformanceMinor
	}
	if int(parsed.Month()) == setting.PerformanceMonth {
		income += setting.AnnualPerformanceMinor
	}
	return income, nil
}

func (a *HengcaiApi) incomeForecastSetting(c *core.WebContext, uid int64) (*hengcai.IncomeForecastSetting, error) {
	var setting hengcai.IncomeForecastSetting
	ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Get(&setting)
	if err != nil {
		return nil, err
	}
	if !ok {
		setting = hengcai.IncomeForecastSetting{Uid: uid, PerformanceMonth: 1}
	}
	return &setting, nil
}

func (a *HengcaiApi) monthlyReconciledIncome(c *core.WebContext, uid int64, month string) (int64, int, error) {
	if _, err := time.Parse("2006-01", month); err != nil {
		return 0, 0, err
	}
	var lines []*hengcai.StatementLine
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND line_kind = ? AND status = ? AND posted_date LIKE ?", uid, lineKindIncome, "POSTED", month+"%").Find(&lines); err != nil {
		return 0, 0, err
	}
	total := int64(0)
	for _, line := range lines {
		total += abs64(line.AmountMinor)
	}
	return total, len(lines), nil
}

func (a *HengcaiApi) buildCashflowProjection(c *core.WebContext, uid int64, month string) (*hengcai.CashflowProjection, error) {
	if _, err := time.Parse("2006-01", month); err != nil {
		return nil, err
	}
	currentMonth := time.Now().Format("2006-01")
	_, expense, transactionCount, err := a.monthlyIncomeExpense(c, uid, month)
	if err != nil {
		return nil, err
	}
	income, reconciledLineCount, err := a.monthlyReconciledIncome(c, uid, month)
	if err != nil {
		return nil, err
	}
	dataType := "ACTUAL"
	quality := "RECONCILED_ACTUAL"
	explanation := fmt.Sprintf("实际收入来自 %d 条已入账对账流水；运营支出来自 %d 条主账本交易", reconciledLineCount, transactionCount)
	if month > currentMonth {
		setting, settingErr := a.incomeForecastSetting(c, uid)
		if settingErr != nil {
			return nil, settingErr
		}
		income, err = forecastIncomeForMonth(setting, month)
		if err != nil {
			return nil, err
		}
		_, expense, err = a.trailingAverageIncomeExpense(c, uid, time.Now(), 3)
		if err != nil {
			return nil, err
		}
		dataType = "FORECAST"
		quality = "SALARY_PERFORMANCE_BASIS"
		explanation = fmt.Sprintf("收入按月薪 %d 分预测", setting.MonthlySalaryMinor)
		if setting.MonthlyPerformanceMinor > 0 {
			explanation += fmt.Sprintf("，并计入月度绩效 %d 分", setting.MonthlyPerformanceMinor)
		}
		projectionMonth, _ := time.Parse("2006-01", month)
		if setting.QuarterlyPerformanceMinor > 0 && int(projectionMonth.Month())%3 == 0 {
			explanation += fmt.Sprintf("，并计入季度绩效 %d 分", setting.QuarterlyPerformanceMinor)
		}
		if setting.AnnualPerformanceMinor > 0 && setting.PerformanceMonth == int(projectionMonth.Month()) {
			explanation += fmt.Sprintf("，并计入年度绩效 %d 分", setting.AnnualPerformanceMinor)
		}
		explanation += "；运营支出采用此前三个月主账本月均值"
		if setting.MonthlySalaryMinor == 0 && setting.MonthlyPerformanceMinor == 0 && setting.QuarterlyPerformanceMinor == 0 && setting.AnnualPerformanceMinor == 0 {
			quality = "INCOME_BASIS_REQUIRED"
			explanation = "尚未设置月薪与绩效，预测收入暂为 0；运营支出采用此前三个月主账本月均值"
		}
	} else if month == currentMonth {
		dataType = "MTD_ACTUAL"
		quality = "RECONCILED_ACTUAL_TO_DATE"
		explanation = fmt.Sprintf("本月至今收入来自 %d 条已入账对账流水；运营支出来自 %d 条主账本交易", reconciledLineCount, transactionCount)
	}
	capexMinor, err := a.monthlyCapexCommitment(c, uid, month, false)
	if err != nil {
		return nil, err
	}
	investmentReturn := int64(0)
	var investment hengcai.InvestmentReturn
	if ok, queryErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ?", uid, month).Get(&investment); queryErr != nil {
		return nil, queryErr
	} else if ok {
		investmentReturn = investment.TotalReturnMinor
	}
	var saved hengcai.CashflowProjection
	if ok, queryErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ?", uid, month).Desc("created_unix_time").Get(&saved); queryErr != nil {
		return nil, queryErr
	} else if ok {
		if saved.InvestmentReturnMinor != 0 {
			investmentReturn = saved.InvestmentReturnMinor
		}
	}
	row := &hengcai.CashflowProjection{Uid: uid, Month: month, DataType: dataType, IncomeMinor: income, OpexMinor: expense, CapexMinor: capexMinor, InvestmentReturnMinor: investmentReturn, EndingInvestableAssetsMinor: saved.EndingInvestableAssetsMinor, Quality: quality, Explanation: explanation + "；CAPEX 来自分期与首付款计划", CreatedUnixTime: time.Now().Unix()}
	row.FreeCashflowMinor = row.IncomeMinor - row.OpexMinor - row.CapexMinor + row.InvestmentReturnMinor
	return row, nil
}

func (a *HengcaiApi) capacity(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	_, averageExpense, err := a.trailingAverageIncomeExpense(c, uid, time.Now(), 3)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	setting, err := a.incomeForecastSetting(c, uid)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	rows := make([]*hengcai.BudgetCapacity, 0, 12)
	now := time.Now()
	for i := 0; i < 12; i++ {
		month := now.AddDate(0, i, 0).Format("2006-01")
		forecastIncome, forecastErr := forecastIncomeForMonth(setting, month)
		if forecastErr != nil {
			return nil, errs.ErrOperationFailed
		}
		committed, commitmentErr := a.monthlyCapexCommitment(c, uid, month, true)
		if commitmentErr != nil {
			return nil, errs.ErrOperationFailed
		}
		row := &hengcai.BudgetCapacity{Uid: uid, Month: month, ReserveMinor: 0, CommittedMinor: committed, AvailableMinor: forecastIncome - averageExpense - committed, HorizonMonths: 12, Status: "SALARY_PERFORMANCE_BASIS", UpdatedUnixTime: now.Unix()}
		updated, updateErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ?", uid, month).Cols("reserve_minor", "committed_minor", "available_minor", "horizon_months", "status", "updated_unix_time").Update(row)
		if updateErr != nil {
			return nil, errs.ErrOperationFailed
		}
		if updated == 0 {
			if _, insertErr := datastore.Container.UserDataStore.Query(c, uid).Insert(row); insertErr != nil {
				return nil, errs.ErrOperationFailed
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (a *HengcaiApi) createCapex(c *core.WebContext) (any, *errs.Error) {
	var input hengcai.CapexPurchase
	if err := c.ShouldBindJSON(&input); err != nil {
		return nil, hcError(err)
	}
	if input.TotalAmountMinor <= 0 || input.InstallmentCount < 1 || strings.TrimSpace(input.ItemName) == "" || input.DownPaymentMinor < 0 || input.DownPaymentMinor > input.TotalAmountMinor || input.InterestFeeTotalMinor < 0 {
		return nil, hcError(errors.New("CAPEX 金额、名称和分期数无效"))
	}
	if _, err := time.Parse("2006-01-02", input.PurchaseDate); err != nil {
		return nil, hcError(errors.New("购买日期无效"))
	}
	if _, err := time.Parse("2006-01-02", input.FirstDueDate); err != nil {
		return nil, hcError(errors.New("首期日期无效"))
	}
	input.Uid = c.GetCurrentUid()
	input.Status = "ACTIVE"
	input.Currency = strings.ToUpper(input.Currency)
	input.FinancingType = strings.ToUpper(strings.TrimSpace(input.FinancingType))
	if input.FinancingType == "" {
		input.FinancingType = "INSTALLMENT"
	}
	if input.Currency == "" {
		input.Currency = "CNY"
	}
	if _, err := datastore.Container.UserDataStore.Query(c, input.Uid).Insert(&input); err != nil {
		return nil, errs.ErrOperationFailed
	}
	principalTotal := input.TotalAmountMinor - input.DownPaymentMinor
	installments := make([]*hengcai.CapexInstallment, 0, input.InstallmentCount)
	for i := 0; i < input.InstallmentCount; i++ {
		dueDate, err := addMonthsToDate(input.FirstDueDate, i)
		if err != nil {
			return nil, errs.ErrOperationFailed
		}
		item := &hengcai.CapexInstallment{Uid: input.Uid, PurchaseId: input.Id, InstallmentNo: i + 1, DueDate: dueDate, PrincipalMinor: splitAmount(principalTotal, input.InstallmentCount, i), InterestMinor: splitAmount(input.InterestFeeTotalMinor, input.InstallmentCount, i), Status: "SCHEDULED"}
		if _, err := datastore.Container.UserDataStore.Query(c, input.Uid).Insert(item); err != nil {
			return nil, errs.ErrOperationFailed
		}
		installments = append(installments, item)
	}
	return map[string]any{"purchase": input, "installments": installments}, nil
}

func (a *HengcaiApi) projectionIncomeBasis(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	setting, err := a.incomeForecastSetting(c, uid)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	actuals := make([]map[string]any, 0, 12)
	now := time.Now()
	for i := 11; i >= 0; i-- {
		month := now.AddDate(0, -i, 0).Format("2006-01")
		income, lineCount, incomeErr := a.monthlyReconciledIncome(c, uid, month)
		if incomeErr != nil {
			return nil, errs.ErrOperationFailed
		}
		var closeRow hengcai.MonthClose
		closed, closeErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ? AND status = ?", uid, month, "CLOSED").Get(&closeRow)
		if closeErr != nil {
			return nil, errs.ErrOperationFailed
		}
		actuals = append(actuals, map[string]any{"month": month, "income_minor": income, "line_count": lineCount, "closed": closed, "source": "RECONCILIATION_POSTED_LINES"})
	}
	return map[string]any{"setting": setting, "actuals": actuals}, nil
}

func (a *HengcaiApi) saveProjectionIncomeBasis(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		MonthlySalaryMinor        int64 `json:"monthly_salary_minor"`
		MonthlyPerformanceMinor   int64 `json:"monthly_performance_minor"`
		QuarterlyPerformanceMinor int64 `json:"quarterly_performance_minor"`
		AnnualPerformanceMinor    int64 `json:"annual_performance_minor"`
		PerformanceMonth          int   `json:"performance_month"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.MonthlySalaryMinor < 0 || input.MonthlyPerformanceMinor < 0 || input.QuarterlyPerformanceMinor < 0 || input.AnnualPerformanceMinor < 0 || input.PerformanceMonth < 1 || input.PerformanceMonth > 12 {
		return nil, hcError(errors.New("月薪、绩效金额或绩效月份无效"))
	}
	uid := c.GetCurrentUid()
	row := &hengcai.IncomeForecastSetting{Uid: uid, MonthlySalaryMinor: input.MonthlySalaryMinor, MonthlyPerformanceMinor: input.MonthlyPerformanceMinor, QuarterlyPerformanceMinor: input.QuarterlyPerformanceMinor, AnnualPerformanceMinor: input.AnnualPerformanceMinor, PerformanceMonth: input.PerformanceMonth, UpdatedUnixTime: time.Now().Unix()}
	updated, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Cols("monthly_salary_minor", "monthly_performance_minor", "quarterly_performance_minor", "annual_performance_minor", "performance_month", "updated_unix_time").Update(row)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	if updated == 0 {
		if _, err := datastore.Container.UserDataStore.Query(c, uid).Insert(row); err != nil {
			return nil, errs.ErrOperationFailed
		}
	}
	return row, nil
}

func (a *HengcaiApi) projections(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	rows := make([]*hengcai.CashflowProjection, 0, 12)
	now := time.Now()
	for i := 0; i < 12; i++ {
		month := now.AddDate(0, i, 0).Format("2006-01")
		row, err := a.buildCashflowProjection(c, uid, month)
		if err != nil {
			return nil, errs.ErrOperationFailed
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (a *HengcaiApi) calculateProjection(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		Month                 string `json:"month"`
		IncomeMinor           int64  `json:"income_minor"`
		OpexMinor             int64  `json:"opex_minor"`
		CapexMinor            int64  `json:"capex_minor"`
		InvestmentReturnMinor int64  `json:"investment_return_minor"`
		EndingAssetsMinor     int64  `json:"ending_assets_minor"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || !validMonth(input.Month) {
		return nil, hcError(errors.New("projection month 无效"))
	}
	row, err := a.buildCashflowProjection(c, c.GetCurrentUid(), input.Month)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	if input.InvestmentReturnMinor != 0 {
		row.InvestmentReturnMinor = input.InvestmentReturnMinor
	}
	if input.EndingAssetsMinor != 0 {
		row.EndingInvestableAssetsMinor = input.EndingAssetsMinor
	}
	row.FreeCashflowMinor = row.IncomeMinor - row.OpexMinor - row.CapexMinor + row.InvestmentReturnMinor
	if _, err := datastore.Container.UserDataStore.Query(c, row.Uid).Insert(row); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return row, nil
}

// Exported route adapters keep the router declaration readable while the
// implementation methods remain grouped by feature above.
func (a *HengcaiApi) PreviewCMBStatementHandler(c *core.WebContext) (any, *errs.Error) {
	return a.previewCMB(c)
}
func (a *HengcaiApi) ConfirmCMBStatementHandler(c *core.WebContext) (any, *errs.Error) {
	return a.confirmCMB(c)
}
func (a *HengcaiApi) StatementListHandler(c *core.WebContext) (any, *errs.Error) {
	return a.listStatements(c)
}
func (a *HengcaiApi) StatementLinesHandler(c *core.WebContext) (any, *errs.Error) {
	return a.getStatementLines(c)
}
func (a *HengcaiApi) ClassifyStatementHandler(c *core.WebContext) (any, *errs.Error) {
	return a.classifyStatement(c)
}
func (a *HengcaiApi) UpdateLineClassificationHandler(c *core.WebContext) (any, *errs.Error) {
	return a.updateLineClassification(c)
}
func (a *HengcaiApi) LinkLineToCapexInstallmentHandler(c *core.WebContext) (any, *errs.Error) {
	return a.linkLineToCapexInstallment(c)
}
func (a *HengcaiApi) PostStatementHandler(c *core.WebContext) (any, *errs.Error) {
	return a.postStatement(c)
}
func (a *HengcaiApi) UnpostStatementHandler(c *core.WebContext) (any, *errs.Error) {
	return a.unpostStatement(c)
}
func (a *HengcaiApi) CloseMonthHandler(c *core.WebContext) (any, *errs.Error) { return a.closeMonth(c) }
func (a *HengcaiApi) InvestmentListHandler(c *core.WebContext) (any, *errs.Error) {
	return a.listInvestments(c)
}
func (a *HengcaiApi) InvestmentSaveHandler(c *core.WebContext) (any, *errs.Error) {
	return a.createInvestment(c)
}
func (a *HengcaiApi) InvestmentDeleteHandler(c *core.WebContext) (any, *errs.Error) {
	return a.deleteInvestment(c)
}
func (a *HengcaiApi) InvestmentLinkHandler(c *core.WebContext) (any, *errs.Error) {
	return a.linkInvestmentAccount(c)
}
func (a *HengcaiApi) MarketPriceListHandler(c *core.WebContext) (any, *errs.Error) {
	return a.listPrices(c)
}
func (a *HengcaiApi) MarketPriceSyncHandler(c *core.WebContext) (any, *errs.Error) {
	return a.syncPrices(c)
}
func (a *HengcaiApi) AlpacaSaveHandler(c *core.WebContext) (any, *errs.Error) { return a.saveAlpaca(c) }
func (a *HengcaiApi) AlpacaGetHandler(c *core.WebContext) (any, *errs.Error)  { return a.getAlpaca(c) }
func (a *HengcaiApi) AlpacaTestHandler(c *core.WebContext) (any, *errs.Error) { return a.testAlpaca(c) }
func (a *HengcaiApi) CapexListHandler(c *core.WebContext) (any, *errs.Error)  { return a.capex(c) }
func (a *HengcaiApi) CapexCreateHandler(c *core.WebContext) (any, *errs.Error) {
	return a.createCapex(c)
}
func (a *HengcaiApi) ProjectionListHandler(c *core.WebContext) (any, *errs.Error) {
	return a.projections(c)
}
func (a *HengcaiApi) ProjectionCalculateHandler(c *core.WebContext) (any, *errs.Error) {
	return a.calculateProjection(c)
}
func (a *HengcaiApi) ProjectionIncomeBasisGetHandler(c *core.WebContext) (any, *errs.Error) {
	return a.projectionIncomeBasis(c)
}
func (a *HengcaiApi) ProjectionIncomeBasisSaveHandler(c *core.WebContext) (any, *errs.Error) {
	return a.saveProjectionIncomeBasis(c)
}
func (a *HengcaiApi) CapexCapacityHandler(c *core.WebContext) (any, *errs.Error) {
	return a.capacity(c)
}
