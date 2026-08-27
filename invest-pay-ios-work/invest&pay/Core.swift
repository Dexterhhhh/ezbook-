import Foundation
import Observation

enum APIError: Error, LocalizedError, Sendable, Equatable {
    case invalidBaseURL
    case invalidResponse
    case transport(String)
    case server(status: Int, code: String, message: String, requestID: String?)
    case decoding(String)

    var errorDescription: String? {
        switch self {
        case .invalidBaseURL:
            "API 地址无效"
        case .invalidResponse:
            "服务器返回了无法识别的响应"
        case .transport(let message):
            "网络连接失败：\(message)"
        case .server(_, _, let message, _):
            message
        case .decoding:
            "服务器数据格式与客户端不兼容"
        }
    }

    var recoverySuggestion: String? {
        switch self {
        case .server(let status, let code, _, _)
            where status == 409 || code == "period_closed":
            "数据已变化或月份已关闭，请刷新后重试。"
        case .transport:
            "请确认 Go API 已启动，并检查设置中的服务器地址。"
        default:
            nil
        }
    }
}

private struct APIEnvelope<Value: Decodable & Sendable>: Decodable, Sendable {
    let data: Value
}

private struct APIErrorEnvelope: Decodable, Sendable {
    struct Payload: Decodable, Sendable {
        let code: String
        let message: String
    }

    let error: Payload
}

private struct APICodingKey: CodingKey, Sendable {
    let stringValue: String
    let intValue: Int?

    init?(stringValue: String) {
        self.stringValue = stringValue
        self.intValue = nil
    }

    init?(intValue: Int) {
        self.stringValue = String(intValue)
        self.intValue = intValue
    }
}

private func swiftPropertyName(for apiKey: String) -> String {
    let components = apiKey.split(separator: "_")
    guard let first = components.first, components.count > 1 else { return apiKey }
    return components.dropFirst().reduce(String(first)) { result, component in
        if component == "id" {
            return result + "ID"
        }
        return result + component.prefix(1).uppercased() + String(component.dropFirst())
    }
}

actor APIClient {
    private let baseURL: URL
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder
    private let bearerToken: String?

    init(
        baseURL: String,
        bearerToken: String? = nil,
        session: URLSession = .shared
    ) throws {
        guard let url = URL(string: baseURL),
              let scheme = url.scheme?.lowercased(),
              ["http", "https"].contains(scheme),
              url.host != nil else {
            throw APIError.invalidBaseURL
        }
        self.baseURL = url
        self.bearerToken = bearerToken
        self.session = session
        self.decoder = JSONDecoder()
        self.decoder.keyDecodingStrategy = .custom { codingPath in
            let apiKey = codingPath.last?.stringValue ?? ""
            return APICodingKey(stringValue: swiftPropertyName(for: apiKey))!
        }
        self.encoder = JSONEncoder()
        self.encoder.keyEncodingStrategy = .convertToSnakeCase
        self.encoder.dateEncodingStrategy = .iso8601
    }

    func health() async throws {
        let _: HealthResponse = try await get("/healthz", wrapped: false)
    }

    func get<Value: Decodable & Sendable>(
        _ path: String,
        query: [URLQueryItem] = [],
        wrapped: Bool = true
    ) async throws -> Value {
        let request = try makeRequest(path: path, query: query)
        return try await perform(request, wrapped: wrapped)
    }

    func send<Body: Encodable & Sendable, Value: Decodable & Sendable>(
        _ path: String,
        method: String,
        body: Body,
        idempotencyKey: String? = nil,
        wrapped: Bool = true
    ) async throws -> Value {
        var request = try makeRequest(path: path)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue(idempotencyKey, forHTTPHeaderField: "Idempotency-Key")
        request.httpBody = try encoder.encode(body)
        return try await perform(request, wrapped: wrapped)
    }

    func send<Value: Decodable & Sendable>(
        _ path: String,
        method: String,
        wrapped: Bool = true
    ) async throws -> Value {
        var request = try makeRequest(path: path)
        request.httpMethod = method
        return try await perform(request, wrapped: wrapped)
    }

    func sendNoContent(_ path: String, method: String) async throws {
        var request = try makeRequest(path: path)
        request.httpMethod = method
        let (_, response): (Data, URLResponse)
        do {
            (_, response) = try await session.data(for: request)
        } catch {
            throw APIError.transport(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.server(
                status: http.statusCode,
                code: "http_\(http.statusCode)",
                message: HTTPURLResponse.localizedString(forStatusCode: http.statusCode),
                requestID: http.value(forHTTPHeaderField: "X-Request-ID")
            )
        }
    }

    func uploadPDF<Value: Decodable & Sendable>(
        _ path: String,
        data: Data,
        fields: [String: String] = [:]
    ) async throws -> Value {
        let boundary = "Boundary-\(UUID().uuidString)"
        var request = try makeRequest(path: path)
        request.httpMethod = "POST"
        request.timeoutInterval = 90
        request.setValue(
            "multipart/form-data; boundary=\(boundary)",
            forHTTPHeaderField: "Content-Type"
        )
        var body = Data()
        for (name, value) in fields.sorted(by: { $0.key < $1.key }) {
            body.appendUTF8("--\(boundary)\r\n")
            body.appendUTF8("Content-Disposition: form-data; name=\"\(name)\"\r\n\r\n")
            body.appendUTF8("\(value)\r\n")
        }
        body.appendUTF8("--\(boundary)\r\n")
        body.appendUTF8("Content-Disposition: form-data; name=\"file\"; filename=\"statement.pdf\"\r\n")
        body.appendUTF8("Content-Type: application/pdf\r\n\r\n")
        body.append(data)
        body.appendUTF8("\r\n--\(boundary)--\r\n")
        request.httpBody = body
        return try await perform(request, wrapped: true)
    }

    private func makeRequest(path: String, query: [URLQueryItem] = []) throws -> URLRequest {
        let cleanPath = path.hasPrefix("/") ? String(path.dropFirst()) : path
        let endpoint = baseURL.appending(path: cleanPath)
        guard var components = URLComponents(url: endpoint, resolvingAgainstBaseURL: false) else {
            throw APIError.invalidBaseURL
        }
        components.queryItems = query.isEmpty ? nil : query
        guard let url = components.url else { throw APIError.invalidBaseURL }
        var request = URLRequest(url: url)
        request.timeoutInterval = 20
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(UUID().uuidString, forHTTPHeaderField: "X-Request-ID")
        if let bearerToken {
            request.setValue("Bearer \(bearerToken)", forHTTPHeaderField: "Authorization")
        }
        return request
    }

    private func perform<Value: Decodable & Sendable>(
        _ request: URLRequest,
        wrapped: Bool
    ) async throws -> Value {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw APIError.transport(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        let requestID = http.value(forHTTPHeaderField: "X-Request-ID")
        guard (200..<300).contains(http.statusCode) else {
            if let payload = try? decoder.decode(APIErrorEnvelope.self, from: data) {
                throw APIError.server(
                    status: http.statusCode,
                    code: payload.error.code,
                    message: payload.error.message,
                    requestID: requestID
                )
            }
            throw APIError.server(
                status: http.statusCode,
                code: "http_\(http.statusCode)",
                message: HTTPURLResponse.localizedString(forStatusCode: http.statusCode),
                requestID: requestID
            )
        }
        do {
            if wrapped {
                return try decoder.decode(APIEnvelope<Value>.self, from: data).data
            }
            return try decoder.decode(Value.self, from: data)
        } catch {
            throw APIError.decoding(error.localizedDescription)
        }
    }
}

@MainActor
@Observable
final class AppStore {
    var baseURL: String
    var selectedMonth: String
    var isConnected = false
    var isAuthenticated = false
    var currentDevice: AuthenticatedDevice?
    var isLoading = false
    var hasLoaded = false
    var dashboard: DashboardData?
    var trendSeries: TrendSeries?
    var expenses: [Expense] = []
    var provisionalEntries: [ProvisionalEntry] = []
    var statements: [StatementRecord] = []
    var financialAccounts: [FinancialAccount] = []
    var postingPreview: StatementPostingPreview?
    var reconciliation: ReconciliationPeriod?
    var importedStatementPreview: CreditCardStatementPreview?
    var positions: [Position] = []
    var investmentAccounts: [InvestmentAccount] = []
    var categories: [ExpenseCategory] = []
    var settings: FinanceSettings?
    var alert: AppAlert?

    @ObservationIgnored private var client: APIClient?
    @ObservationIgnored private var importedStatementData: Data?

    init(
        baseURL: String = UserDefaults.standard.string(forKey: "finance.apiBaseURL")
            ?? "http://127.0.0.1:8080",
        selectedMonth: String = Month.current
    ) {
        self.baseURL = baseURL
        self.selectedMonth = selectedMonth
        let token = KeychainStore.token(for: baseURL)
        self.isAuthenticated = token != nil
        self.client = try? APIClient(baseURL: baseURL, bearerToken: token)
    }

    func saveBaseURL() async {
        let trimmed = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
            .trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        do {
            let publicClient = try APIClient(baseURL: trimmed)
            try await publicClient.health()
            let token = KeychainStore.token(for: trimmed)
            baseURL = trimmed
            client = try APIClient(baseURL: trimmed, bearerToken: token)
            UserDefaults.standard.set(trimmed, forKey: "finance.apiBaseURL")
            isAuthenticated = token != nil
            if token == nil {
                isConnected = false
                hasLoaded = true
                alert = .success("服务器可访问，请使用 API Key 和密码登录")
            } else {
                alert = .success("连接成功")
                await refreshAll()
            }
        } catch {
            present(error)
        }
    }

    func login(apiKey: String, password: String, deviceName: String) async -> Bool {
        do {
            let publicClient = try APIClient(baseURL: baseURL)
            let request = LoginRequest(
                apiKey: apiKey,
                password: password,
                deviceName: deviceName.trimmingCharacters(in: .whitespacesAndNewlines),
                deviceType: "IPHONE"
            )
            let session: DeviceSession = try await publicClient.send(
                "/api/v1/auth/login",
                method: "POST",
                body: request
            )
            try KeychainStore.save(token: session.token, for: baseURL)
            client = try APIClient(baseURL: baseURL, bearerToken: session.token)
            currentDevice = session.device
            isAuthenticated = true
            alert = .success("设备认证成功")
            await refreshAll()
            return true
        } catch {
            present(error)
            return false
        }
    }

    func revokeCurrentDevice() async {
        guard let client else { return }
        do {
            try await client.sendNoContent(
                "/api/v1/auth/device",
                method: "DELETE"
            )
            clearAuthentication()
            alert = .success("本设备已撤销")
        } catch {
            present(error)
        }
    }

    func refreshAll() async {
        guard !isLoading else { return }
        guard isAuthenticated else {
            hasLoaded = true
            isConnected = false
            return
        }
        guard let client else {
            present(APIError.invalidBaseURL)
            return
        }
        isLoading = true
        defer {
            isLoading = false
            hasLoaded = true
        }
        do {
            try await client.health()
            isConnected = true
            async let dashboard: DashboardData = client.get(
                "/api/v1/dashboard",
                query: [.init(name: "month", value: selectedMonth)]
            )
            async let trends: TrendSeries = client.get(
                "/api/v1/trends",
                query: [.init(name: "from", value: Month.offset(selectedMonth, by: -6))]
            )
            async let expenses: [Expense] = client.get(
                "/api/v1/expenses",
                query: [.init(name: "month", value: selectedMonth)]
            )
            async let provisional: [ProvisionalEntry] = client.get(
                "/api/v1/provisional-entries",
                query: [.init(name: "month", value: selectedMonth)]
            )
            async let statements: [StatementRecord] = client.get(
                "/api/v1/statements",
                query: [.init(name: "month", value: selectedMonth)]
            )
            async let financialAccounts: [FinancialAccount] = client.get("/api/v1/financial-accounts")
            async let positions: [Position] = client.get("/api/v1/positions")
            async let accounts: [InvestmentAccount] = client.get("/api/v1/investment-accounts")
            async let categories: [ExpenseCategory] = client.get(
                "/api/v1/categories",
                query: [.init(name: "cashflow_type", value: "OPEX")]
            )
            async let settings: FinanceSettings = client.get("/api/v1/settings")
            async let device: AuthenticatedDevice = client.get("/api/v1/auth/device")

            self.dashboard = try await dashboard
            self.trendSeries = try await trends
            self.expenses = try await expenses
            self.provisionalEntries = try await provisional
            self.statements = try await statements
            self.financialAccounts = try await financialAccounts
            self.positions = try await positions
            self.investmentAccounts = try await accounts
            self.categories = try await categories.filter { $0.depth == 1 }
            self.settings = try await settings
            self.currentDevice = try await device
            await refreshStatementReview(showError: false)
        } catch {
            isConnected = false
            present(error)
        }
    }

    func changeMonth(by value: Int) async {
        selectedMonth = Month.offset(selectedMonth, by: value)
        await refreshAll()
    }

    func addProvisional(_ draft: ProvisionalDraft) async -> Bool {
        guard let client else { return false }
        do {
            let payload = ProvisionalCreateRequest(draft: draft)
            let _: ProvisionalEntry = try await client.send(
                "/api/v1/provisional-entries",
                method: "POST",
                body: payload,
                idempotencyKey: payload.idempotencyKey
            )
            alert = .success("已保存为临时记录，待账单校准")
            await refreshAll()
            return true
        } catch {
            present(error)
            return false
        }
    }

    func confirmCashEntry(_ entry: ProvisionalEntry) async {
        guard entry.paymentMethod.uppercased() == "CASH",
              let categoryID = entry.categoryID,
              let itemName = entry.itemName,
              let client else { return }
        do {
            let body = ConfirmProvisionalRequest(
                categoryID: categoryID,
                itemName: itemName,
                note: nil
            )
            let _: MutationResponse = try await client.send(
                "/api/v1/provisional-entries/\(entry.id)/confirm",
                method: "POST",
                body: body
            )
            alert = .success("现金记录已进入正式账本")
            await refreshAll()
        } catch {
            present(error)
        }
    }

    func refreshStatementReview(showError: Bool = true) async {
        guard let client else { return }
        do {
            async let preview: StatementPostingPreview = client.get(
                "/api/v1/statement-postings/preview",
                query: [.init(name: "month", value: selectedMonth)]
            )
            async let period: ReconciliationPeriod = client.get(
                "/api/v1/reconciliation-periods/\(selectedMonth)"
            )
            postingPreview = try await preview
            reconciliation = try await period
        } catch {
            postingPreview = nil
            reconciliation = nil
            if showError { present(error) }
        }
    }

    func previewStatement(data: Data) async {
        guard let client else { return }
        do {
            let preview: CreditCardStatementPreview = try await client.uploadPDF(
                "/api/v1/statement-imports/cmb-credit-card/preview",
                data: data
            )
            importedStatementData = data
            importedStatementPreview = preview
        } catch {
            present(error)
        }
    }

    func confirmStatementImport(accountID: String) async -> Bool {
        guard let client, let importedStatementData else { return false }
        do {
            let _: StatementRecord = try await client.uploadPDF(
                "/api/v1/statement-imports/cmb-credit-card/confirm",
                data: importedStatementData,
                fields: ["account_id": accountID]
            )
            self.importedStatementData = nil
            self.importedStatementPreview = nil
            alert = .success("账单已导入，接下来请按自然月审核")
            await refreshAll()
            return true
        } catch {
            present(error)
            return false
        }
    }

    func cancelStatementImport() {
        importedStatementData = nil
        importedStatementPreview = nil
    }

    func postStatementLines() async {
        guard let client else { return }
        do {
            let body = StatementPostRequest(month: selectedMonth, confirmReplace: true)
            let _: EmptyResponse = try await client.send(
                "/api/v1/statement-postings/confirm",
                method: "POST",
                body: body
            )
            alert = .success("账单已推送至正式账本")
            await refreshAll()
        } catch {
            present(error)
        }
    }

    func closeMonth() async {
        guard let client else { return }
        do {
            let _: EmptyResponse = try await client.send(
                "/api/v1/periods/\(selectedMonth)/close",
                method: "POST"
            )
            alert = .success("\(selectedMonth) 已月结")
            await refreshAll()
        } catch {
            present(error)
        }
    }

    func dismissAlert() {
        alert = nil
    }

    private func present(_ error: Error) {
        let apiError = error as? APIError
        if case .server(let status, _, _, _)? = apiError, status == 401 {
            clearAuthentication()
        }
        alert = AppAlert(
            title: "操作失败",
            message: [error.localizedDescription, apiError?.recoverySuggestion]
                .compactMap(\.self)
                .joined(separator: "\n")
        )
    }

    private func clearAuthentication() {
        KeychainStore.removeToken(for: baseURL)
        isAuthenticated = false
        isConnected = false
        currentDevice = nil
        dashboard = nil
        trendSeries = nil
        expenses = []
        provisionalEntries = []
        statements = []
        financialAccounts = []
        positions = []
        investmentAccounts = []
        categories = []
        settings = nil
        client = try? APIClient(baseURL: baseURL)
    }

    static var preview: AppStore {
        let store = AppStore()
        store.hasLoaded = true
        store.isConnected = true
        store.dashboard = .preview
        store.trendSeries = .preview
        store.expenses = [.preview]
        store.positions = [.preview]
        return store
    }
}

private extension Data {
    mutating func appendUTF8(_ value: String) {
        append(contentsOf: value.utf8)
    }
}

struct AppAlert: Identifiable, Sendable {
    let id = UUID()
    let title: String
    let message: String

    static func success(_ message: String) -> AppAlert {
        AppAlert(title: "完成", message: message)
    }
}
