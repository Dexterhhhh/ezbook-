import Foundation

struct HealthResponse: Decodable, Sendable {
    let status: String
}

struct EmptyResponse: Decodable, Sendable {}

struct AuthenticatedDevice: Decodable, Sendable {
    let id: String
    let deviceName: String
    let deviceType: String
    let lastSeenAt: String?
    let isActive: Bool
    let createdAt: String
}

struct DeviceSession: Decodable, Sendable {
    let token: String
    let device: AuthenticatedDevice
}

struct LoginRequest: Encodable, Sendable {
    let apiKey: String
    let password: String
    let deviceName: String
    let deviceType: String
}

struct MutationResponse: Decodable, Sendable {
    let id: String?
    let version: Int64?
}

struct DashboardData: Decodable, Sendable {
    let month: String
    let periodStatus: String
    let salaryAmountMinor: Int64?
    let actualOpexMinor: Int64
    let normalizedOpexMinor: Int64?
    let capexMinor: Int64
    let fcfMinor: Int64?
    let assetChangeMinor: Int64?
    let unclassifiedItemCount: Int
    let dataType: String
    let dataQualityStatus: String
    let endingInvestableAssetsMinor: Int64?
    let confirmedInvestmentReturnMinor: Int64

    static let preview = DashboardData(
        month: Month.current,
        periodStatus: "DRAFT",
        salaryAmountMinor: 30_000_00,
        actualOpexMinor: 8_650_00,
        normalizedOpexMinor: 7_980_00,
        capexMinor: 2_000_00,
        fcfMinor: 19_350_00,
        assetChangeMinor: 19_350_00,
        unclassifiedItemCount: 0,
        dataType: "ACTUAL",
        dataQualityStatus: "READY",
        endingInvestableAssetsMinor: 600_000_00,
        confirmedInvestmentReturnMinor: 0
    )
}

struct TrendSeries: Decodable, Sendable {
    let points: [TrendPoint]
    let actualMonthCount: Int
    let closedHistoryMonthCount: Int
    let forecastMonthCount: Int
    let opexHistoryMonthsRequired: Int
    let currentMonth: String

    static let preview = TrendSeries(
        points: [
            TrendPoint(month: "2026-05", dataType: "ACTUAL", periodStatus: "CLOSED", salaryAmountMinor: 3_000_000, otherIncomeAmountMinor: 0, opexMinor: 850_000, capexMinor: 0, fcfMinor: 2_150_000, endingInvestableAssetsMinor: 55_000_000, dataQualityStatus: "READY", source: "ledger"),
            TrendPoint(month: "2026-06", dataType: "ACTUAL", periodStatus: "CLOSED", salaryAmountMinor: 3_000_000, otherIncomeAmountMinor: 0, opexMinor: 900_000, capexMinor: 100_000, fcfMinor: 2_000_000, endingInvestableAssetsMinor: 57_000_000, dataQualityStatus: "READY", source: "ledger"),
            TrendPoint(month: "2026-07", dataType: "ACTUAL", periodStatus: "DRAFT", salaryAmountMinor: 3_000_000, otherIncomeAmountMinor: 0, opexMinor: 865_000, capexMinor: 200_000, fcfMinor: 1_935_000, endingInvestableAssetsMinor: 58_935_000, dataQualityStatus: "READY", source: "ledger")
        ],
        actualMonthCount: 3,
        closedHistoryMonthCount: 2,
        forecastMonthCount: 0,
        opexHistoryMonthsRequired: 3,
        currentMonth: "2026-07"
    )
}

struct TrendPoint: Decodable, Identifiable, Sendable {
    var id: String { month }
    let month: String
    let dataType: String
    let periodStatus: String
    let salaryAmountMinor: Int64?
    let otherIncomeAmountMinor: Int64
    let opexMinor: Int64?
    let capexMinor: Int64?
    let fcfMinor: Int64?
    let endingInvestableAssetsMinor: Int64?
    let dataQualityStatus: String
    let source: String
}

struct ExpenseCategory: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let name: String
    let depth: Int
    let defaultCashflowType: String
    let isSystem: Bool?
}

struct Expense: Decodable, Identifiable, Sendable {
    let id: String
    let occurredOn: String
    let merchantName: String?
    let totalAmountMinor: Int64
    let classificationStatus: String
    let items: [ExpenseItem]

    static let preview = Expense(
        id: UUID().uuidString,
        occurredOn: "2026-07-28",
        merchantName: "示例商户",
        totalAmountMinor: 12_800,
        classificationStatus: "CLASSIFIED",
        items: []
    )
}

struct ExpenseItem: Decodable, Identifiable, Sendable {
    let id: String
    let itemName: String
    let categoryName: String
    let cashflowType: String
    let netAmountMinor: Int64
    let normalizationAdjustmentMinor: Int64?
    let isExceptional: Bool
}

struct ProvisionalEntry: Decodable, Identifiable, Sendable {
    let id: String
    let occurredAt: String
    let merchantName: String?
    let amountMinor: Int64
    let currency: String
    let status: String
    let paymentMethod: String
    let categoryID: String?
    let itemName: String?
    let isExceptional: Bool?
}

struct StatementRecord: Decodable, Identifiable, Sendable {
    let id: String
    let accountID: String
    let statementPeriodStart: String
    let statementPeriodEnd: String
    let closingBalanceMinor: Int64
    let balanceValid: Bool
    let reconciliationStatus: String
    let lineCount: Int
    let unmatchedLineCount: Int
    let version: Int
}

struct FinancialAccount: Decodable, Identifiable, Sendable {
    let id: String
    let accountType: String
    let institutionName: String
    let accountName: String
    let accountLastFour: String?
    let currency: String
    let normalBalanceType: String
    let isActive: Bool
    let version: Int64
}

struct CreditCardStatementPreview: Decodable, Sendable {
    let provider: String
    let statementDate: String
    let paymentDueDate: String
    let statementPeriodStart: String
    let statementPeriodEnd: String
    let openingBalanceMinor: Int64
    let closingBalanceMinor: Int64
    let minimumPaymentMinor: Int64
    let balanceValid: Bool
    let summaryValid: Bool
    let needsReview: Bool
    let validationErrors: [String]
    let lines: [CreditCardStatementPreviewLine]

    var canConfirm: Bool {
        balanceValid && summaryValid && validationErrors.isEmpty
    }
}

struct CreditCardStatementPreviewLine: Decodable, Sendable {
    let transactionDate: String?
    let postedDate: String
    let description: String
    let postingAmountMinor: Int64
    let lineKind: String
    let accountingTreatment: String
}

struct StatementPostingPreview: Decodable, Sendable {
    let month: String
    let statementCount: Int
    let accountCount: Int
    let eligibleExpenseCount: Int
    let eligibleAmountMinor: Int64
    let pendingClassificationCount: Int
    let excludedLineCount: Int
    let alreadyHandledCount: Int
    let manualReplacementCount: Int
    let manualReplacementMinor: Int64
    let canPost: Bool
    let lines: [StatementPostingLine]
}

struct StatementPostingLine: Decodable, Identifiable, Sendable {
    let id: String
    let statementID: String
    let accountName: String
    let effectiveDate: String
    let description: String
    let amountMinor: Int64
    let direction: String
    let lineKind: String
    let categoryID: String?
    let categoryName: String?
    let isExceptional: Bool
    let alreadyHandled: Bool
    let editable: Bool
    let eligible: Bool
    let exclusionReason: String?
}

struct ReconciliationPeriod: Decodable, Sendable {
    let month: String
    let status: String
    let requiredAccountCount: Int
    let receivedStatementCount: Int
    let unmatchedLineCount: Int
    let unmatchedProvisionalCount: Int
    let invalidStatementCount: Int
    let canClose: Bool
}

struct Position: Decodable, Identifiable, Sendable {
    var id: String { "\(accountID)-\(instrumentID)" }
    let accountID: String
    let instrumentID: String
    let symbol: String
    let name: String
    let assetType: String
    let quantity: String
    let positionSide: String
    let currency: String
    let markPrice: String?
    let marketValueMinor: Int64?
    let priceQuality: String?

    static let preview = Position(
        accountID: "account",
        instrumentID: "instrument",
        symbol: "VOO",
        name: "Vanguard S&P 500 ETF",
        assetType: "ETF",
        quantity: "10",
        positionSide: "LONG",
        currency: "USD",
        markPrice: "512.30",
        marketValueMinor: 512_300,
        priceQuality: "LIVE"
    )
}

struct InvestmentAccount: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let institution: String?
    let accountType: String
    let baseCurrency: String
    let isActive: Bool
}

struct FinanceSettings: Decodable, Sendable {
    let openingAssetAmountMinor: Int64?
    let openingBalanceMonth: String?
    let forecastMonths: Int
    let opexLookbackMonths: Int
    let opexAnnualGrowthBps: Int64
    let calculationVersion: String
    let version: Int64
}

struct ProvisionalDraft: Sendable {
    let occurredAt: Date
    let amountMinor: Int64
    let merchantName: String
    let itemName: String
    let paymentMethod: String
    let categoryID: String
    let isExceptional: Bool
    let note: String?
}

struct ProvisionalCreateRequest: Encodable, Sendable {
    struct Item: Encodable, Sendable {
        let categoryID: String
        let itemName: String
        let quantity: Int
        let amountMinor: Int64
        let confidenceBps: Int
        let isExceptional: Bool
    }

    let sourceType = "IPHONE"
    let idempotencyKey: String
    let observedAt: Date
    let occurredAt: Date
    let amountMinor: Int64
    let currency = "CNY"
    let entryType = "EXPENSE"
    let paymentMethod: String
    let confidenceBps = 10_000
    let merchantName: String
    let note: String?
    let items: [Item]

    init(draft: ProvisionalDraft) {
        let key = UUID().uuidString.lowercased()
        idempotencyKey = "ios-\(key)"
        observedAt = Date()
        occurredAt = draft.occurredAt
        amountMinor = -abs(draft.amountMinor)
        paymentMethod = draft.paymentMethod
        merchantName = draft.merchantName
        note = draft.note
        items = [
            Item(
                categoryID: draft.categoryID,
                itemName: draft.itemName,
                quantity: 1,
                amountMinor: abs(draft.amountMinor),
                confidenceBps: 10_000,
                isExceptional: draft.isExceptional
            )
        ]
    }
}

struct ConfirmProvisionalRequest: Encodable, Sendable {
    let categoryID: String
    let itemName: String
    let note: String?
}

struct StatementPostRequest: Encodable, Sendable {
    let month: String
    let confirmReplace: Bool
}

enum Money {
    static func decimal(minor: Int64) -> Decimal {
        Decimal(minor) / 100
    }

    static func parseMinor(_ text: String) -> Int64? {
        let normalized = text
            .replacingOccurrences(of: ",", with: "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard let decimal = Decimal(string: normalized, locale: Locale(identifier: "en_US_POSIX")) else {
            return nil
        }
        var value = decimal * 100
        var rounded = Decimal()
        NSDecimalRound(&rounded, &value, 0, .plain)
        return NSDecimalNumber(decimal: rounded).int64Value
    }

    static func format(_ minor: Int64?, currency: String = "CNY") -> String {
        guard let minor else { return "—" }
        return decimal(minor: minor).formatted(
            .currency(code: currency)
                .precision(.fractionLength(2))
        )
    }
}

enum Month {
    private static let formatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(identifier: "Asia/Shanghai")
        formatter.dateFormat = "yyyy-MM"
        return formatter
    }()

    static var current: String {
        formatter.string(from: Date())
    }

    static func offset(_ month: String, by value: Int) -> String {
        guard let date = formatter.date(from: month),
              let result = Calendar(identifier: .gregorian)
                .date(byAdding: .month, value: value, to: date) else {
            return month
        }
        return formatter.string(from: result)
    }
}

extension Array where Element == StatementPostingLine {
    func sortedByAbsoluteAmount(ascending: Bool) -> [Element] {
        sorted {
            ascending
                ? abs($0.amountMinor) < abs($1.amountMinor)
                : abs($0.amountMinor) > abs($1.amountMinor)
        }
    }
}
