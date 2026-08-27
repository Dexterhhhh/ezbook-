//
//  invest_payTests.swift
//  invest&payTests
//
//  Created by the project contributors.
//

import Foundation
import Testing
@testable import invest_pay

struct HengCaiTests {
    @Test("金额解析使用最小货币单位")
    func moneyParsing() {
        #expect(Money.parseMinor("128.00") == 12_800)
        #expect(Money.parseMinor("1,234.56") == 123_456)
        #expect(Money.parseMinor("0.005") == 1)
        #expect(Money.parseMinor("abc") == nil)
    }

    @Test("月份偏移跨年正确")
    func monthOffset() {
        #expect(Month.offset("2026-01", by: -1) == "2025-12")
        #expect(Month.offset("2026-12", by: 1) == "2027-01")
    }

    @Test("账单金额排序使用绝对值")
    func statementAbsoluteSorting() {
        let low = line(id: "low", amount: -500)
        let high = line(id: "high", amount: 10_000)
        let refund = line(id: "refund", amount: -2_000)
        let values = [low, high, refund]

        #expect(values.sortedByAbsoluteAmount(ascending: true).map(\.id) == ["low", "refund", "high"])
        #expect(values.sortedByAbsoluteAmount(ascending: false).map(\.id) == ["high", "refund", "low"])
    }

    @Test("API 错误保留冲突提示")
    func conflictError() {
        let error = APIError.server(
            status: 409,
            code: "version_conflict",
            message: "记录已变化",
            requestID: "request-id"
        )
        #expect(error.errorDescription == "记录已变化")
        #expect(error.recoverySuggestion?.contains("刷新") == true)
    }

    @Test("登录请求编码不泄漏到用户偏好")
    func loginRequestEncoding() throws {
        let request = LoginRequest(
            apiKey: "test-key",
            password: "test-password",
            deviceName: "测试 iPhone",
            deviceType: "IPHONE"
        )
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        let object = try #require(
            JSONSerialization.jsonObject(with: encoder.encode(request)) as? [String: String]
        )
        #expect(object["api_key"] == "test-key")
        #expect(object["device_type"] == "IPHONE")
        #expect(UserDefaults.standard.string(forKey: "finance.apiKey") == nil)
        #expect(UserDefaults.standard.string(forKey: "finance.apiPassword") == nil)
    }

    @Test("本地 API 响应兼容当前 Codable 模型")
    func liveAPIContract() async throws {
        let environment = ProcessInfo.processInfo.environment
        guard let apiKey = environment["HENGCAI_TEST_API_KEY"],
              let password = environment["HENGCAI_TEST_API_PASSWORD"] else {
            return
        }

        let publicClient = try APIClient(baseURL: "http://127.0.0.1:8080")
        let session: DeviceSession = try await publicClient.send(
            "/api/v1/auth/login",
            method: "POST",
            body: LoginRequest(
                apiKey: apiKey,
                password: password,
                deviceName: "Swift Contract Validation",
                deviceType: "IPHONE"
            )
        )
        let client = try APIClient(
            baseURL: "http://127.0.0.1:8080",
            bearerToken: session.token
        )

        let _: DashboardData = try await client.get(
            "/api/v1/dashboard",
            query: [.init(name: "month", value: Month.current)]
        )
        let _: TrendSeries = try await client.get(
            "/api/v1/trends",
            query: [.init(name: "from", value: Month.offset(Month.current, by: -6))]
        )
        let _: [Expense] = try await client.get(
            "/api/v1/expenses",
            query: [.init(name: "month", value: Month.current)]
        )
        let _: [ProvisionalEntry] = try await client.get(
            "/api/v1/provisional-entries",
            query: [.init(name: "month", value: Month.current)]
        )
        let _: [StatementRecord] = try await client.get(
            "/api/v1/statements",
            query: [.init(name: "month", value: Month.current)]
        )
        let _: [FinancialAccount] = try await client.get("/api/v1/financial-accounts")
        let _: [Position] = try await client.get("/api/v1/positions")
        let _: [InvestmentAccount] = try await client.get("/api/v1/investment-accounts")
        let _: [ExpenseCategory] = try await client.get(
            "/api/v1/categories",
            query: [.init(name: "cashflow_type", value: "OPEX")]
        )
        let _: FinanceSettings = try await client.get("/api/v1/settings")
        let _: AuthenticatedDevice = try await client.get("/api/v1/auth/device")
        try await client.sendNoContent("/api/v1/auth/device", method: "DELETE")
    }

    private func line(id: String, amount: Int64) -> StatementPostingLine {
        StatementPostingLine(
            id: id,
            statementID: "statement",
            accountName: "信用卡",
            effectiveDate: "2026-07-01",
            description: "测试",
            amountMinor: amount,
            direction: amount < 0 ? "CREDIT" : "DEBIT",
            lineKind: "PURCHASE",
            categoryID: nil,
            categoryName: nil,
            isExceptional: false,
            alreadyHandled: false,
            editable: true,
            eligible: true,
            exclusionReason: nil
        )
    }
}
