//
//  invest_payApp.swift
//  invest&pay
//
//  Created by the project contributors.
//

import SwiftUI

@main
struct HengCaiApp: App {
    @State private var store = AppStore()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environment(store)
        }
    }
}
