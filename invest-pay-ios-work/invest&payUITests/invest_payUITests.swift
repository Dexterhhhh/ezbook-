//
//  invest_payUITests.swift
//  invest&payUITests
//
//  Created by the project contributors.
//

import XCTest

final class HengCaiUITests: XCTestCase {

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    @MainActor
    func testMainNavigation() throws {
        let app = XCUIApplication()
        app.launch()

        let tabBar = try openSettings(in: app)
        for title in ["概览", "记账", "账单", "趋势"] {
            XCTAssertTrue(tabBar.buttons[title].exists, "缺少 \(title) Tab")
        }
        let loginButton = app.buttons["登录并绑定本设备"]
        let revokeButton = app.buttons["撤销本设备"]
        XCTAssertTrue(
            loginButton.waitForExistence(timeout: 2) || revokeButton.exists,
            "设置页缺少认证入口"
        )
    }

    @MainActor
    func testAuthenticatedSessionLifecycle() throws {
        let environment = ProcessInfo.processInfo.environment
        guard let apiKey = environment["HENGCAI_TEST_API_KEY"],
              let password = environment["HENGCAI_TEST_API_PASSWORD"] else {
            throw XCTSkip("未提供本地认证测试凭据")
        }

        let app = XCUIApplication()
        app.launchEnvironment["HENGCAI_DEVICE_NAME_OVERRIDE"] = "HengCai UI Validation"
        app.launchEnvironment["HENGCAI_API_KEY_OVERRIDE"] = apiKey
        app.launchEnvironment["HENGCAI_API_PASSWORD_OVERRIDE"] = password
        app.launch()
        _ = try openSettings(in: app)

        if app.buttons["撤销本设备"].exists {
            app.buttons["撤销本设备"].tap()
            dismissCompletionAlert(in: app)
        }

        let keyField = app.secureTextFields["API Key"]
        let passwordField = app.secureTextFields["密码"]
        XCTAssertTrue(keyField.waitForExistence(timeout: 3))
        XCTAssertTrue(passwordField.exists)
        app.buttons["登录并绑定本设备"].tap()

        dismissCompletionAlert(in: app, timeout: 12)
        XCTAssertTrue(
            app.staticTexts["已通过设备令牌认证"].waitForExistence(timeout: 8),
            "登录后未显示设备认证状态"
        )
        XCTAssertTrue(app.staticTexts["API, 已连接"].waitForExistence(timeout: 5))

        app.buttons["撤销本设备"].tap()
        dismissCompletionAlert(in: app)
        XCTAssertTrue(app.buttons["登录并绑定本设备"].waitForExistence(timeout: 5))
    }

    @MainActor
    func testLaunchPerformance() throws {
        // This measures how long it takes to launch your application.
        measure(metrics: [XCTApplicationLaunchMetric()]) {
            XCUIApplication().launch()
        }
    }

    @MainActor
    private func openSettings(in app: XCUIApplication) throws -> XCUIElement {
        let tabBar = app.tabBars.firstMatch
        XCTAssertTrue(tabBar.waitForExistence(timeout: 5))
        if app.alerts.buttons["好"].waitForExistence(timeout: 1) {
            app.alerts.buttons["好"].tap()
        }

        let moreButton = tabBar.buttons["More"].exists
            ? tabBar.buttons["More"]
            : tabBar.buttons["更多"]
        XCTAssertTrue(moreButton.exists, "缺少“更多”Tab")
        moreButton.tap()
        XCTAssertTrue(app.staticTexts["投资"].waitForExistence(timeout: 2))
        let settingsEntry = app.staticTexts["设置"]
        XCTAssertTrue(settingsEntry.exists, "“更多”中缺少设置入口")
        settingsEntry.tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 2))
        return tabBar
    }

    @MainActor
    private func dismissCompletionAlert(in app: XCUIApplication, timeout: TimeInterval = 5) {
        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: timeout), "操作完成提示未出现")
        XCTAssertEqual(
            alert.label,
            "完成",
            "收到非成功提示：\(alert.debugDescription)"
        )
        alert.buttons["好"].tap()
    }
}
