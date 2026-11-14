# The ply iOS app. Everything builds from here, with Xcode 26+ and XcodeGen
# (brew install xcodegen):
#
#   make            the project, then a simulator build
#   make test       the core unit tests on this Mac (no simulator needed)
#   make run        build, boot the simulator, install and launch
#   make tv         the Apple TV app, built for the tvOS simulator
#   make tv-run     build, boot the TV simulator, install and launch
#   make device TEAM=ABCDE12345   a build for a real device, signed by your team
#   make open       the project in Xcode
#   make clean
#
# SIM picks the simulator: a name (the first of that name `xcrun simctl list devices` shows, so the
# oldest runtime) or a UDID, as in make run SIM="iPhone 17 Pro". TEAM is your Apple developer team
# id; `make device` saves it in .team (ignored by git), so a later regeneration keeps signing as well.
#
# Xcode 26 (Swift 6.2) is required: project.yml sets SWIFT_DEFAULT_ACTOR_ISOLATION and the sources
# use `nonisolated` on type declarations, neither of which an older toolchain knows.

SIM     ?= iPhone 16 Pro
TVSIM   ?= Apple TV
# The team of the last `make device`, so that regenerating the project keeps signing.
TEAM    ?= $(shell cat .team 2>/dev/null)
BUNDLE  := com.ply.ios
TVBUNDLE := com.ply.tvos
DERIVED := build/DerivedData
APP     := $(DERIVED)/Build/Products/Debug-iphonesimulator/Ply.app
TVAPP   := $(DERIVED)/Build/Products/Debug-appletvsimulator/Ply.app
XCB     := xcodebuild -project Ply.xcodeproj -scheme Ply -derivedDataPath $(DERIVED) -quiet
XCB_TV  := xcodebuild -project Ply.xcodeproj -scheme PlyTV -derivedDataPath $(DERIVED) -quiet
XCB_CORE := xcodebuild -project Ply.xcodeproj -scheme PlyCore -derivedDataPath $(DERIVED) -quiet
lp      := (
rp      := )
SIMID   := $(shell xcrun simctl list devices available | grep -F -e "    $(SIM) $(lp)" -e "$(lp)$(SIM)$(rp)" | grep -oE '[0-9A-F]{8}(-[0-9A-F]{4}){3}-[0-9A-F]{12}' | head -1)
SIMDEST := -destination 'id=$(SIMID)'
TVSIMID := $(shell xcrun simctl list devices available | grep -F -e "    $(TVSIM) $(lp)" -e "$(lp)$(TVSIM)$(rp)" | grep -oE '[0-9A-F]{8}(-[0-9A-F]{4}){3}-[0-9A-F]{12}' | head -1)

.PHONY: all project build test ci-build run tv tv-run device open clean simulator toolchain

all: build

# Regenerated whenever the spec or any source file (added, removed, changed) is newer. A name with
# a space (an asset catalog's "App Icon - App Store.imagestack") is escaped here, since make would
# split it; no other shell character in a file name is escaped.
Ply.xcodeproj: project.yml $(shell find Ply PlyTV PlyCoreTests -print | sed 's/ /\\ /g')
	@$(MAKE) --no-print-directory toolchain
	PLY_TEAM="$(TEAM)" xcodegen generate --quiet
	@touch $@

project: Ply.xcodeproj

toolchain:
	@command -v xcodegen >/dev/null || { echo "xcodegen is missing: brew install xcodegen"; exit 1; }
	@major=`xcodebuild -version 2>/dev/null | awk '/Xcode/ {print $$2}' | cut -d. -f1`; \
		test -n "$$major" || { echo "Xcode is missing: install Xcode 26 or newer"; exit 1; }; \
		test "$$major" -ge 26 || { echo "Xcode $$major is too old: this app needs Xcode 26 or newer (Swift 6.2)"; exit 1; }

simulator:
	@test -n "$(SIMID)" || { echo "no simulator named $(SIM): pick one of xcrun simctl list devices available"; exit 1; }

build: Ply.xcodeproj simulator toolchain
	$(XCB) $(SIMDEST) build

# CI has no simulator to boot: a generic destination compiles the app for the simulator platform.
ci-build: Ply.xcodeproj toolchain
	$(XCB) -destination 'generic/platform=iOS Simulator' build

# The core tests are a macOS unit-test bundle: no simulator, no test host, no debugger.
test: Ply.xcodeproj toolchain
	$(XCB_CORE) -destination 'platform=macOS' test

run: build
	xcrun simctl boot $(SIMID) 2>/dev/null || true
	open -a Simulator 2>/dev/null || true
	xcrun simctl install $(SIMID) "$(APP)"
	xcrun simctl launch $(SIMID) $(BUNDLE)

tv: Ply.xcodeproj toolchain
	$(XCB_TV) -destination 'generic/platform=tvOS Simulator' build

tv-run: tv
	@test -n "$(TVSIMID)" || { echo "no tvOS simulator named $(TVSIM): pick one of xcrun simctl list devices available"; exit 1; }
	xcrun simctl boot $(TVSIMID) 2>/dev/null || true
	open -a Simulator 2>/dev/null || true
	xcrun simctl install $(TVSIMID) "$(TVAPP)"
	xcrun simctl launch $(TVSIMID) $(TVBUNDLE)

device:
	@test -n "$(TEAM)" || { echo "make device needs TEAM=<your Apple developer team id>"; exit 1; }
	@$(MAKE) --no-print-directory toolchain
	@echo "$(TEAM)" > .team
	PLY_TEAM="$(TEAM)" xcodegen generate --quiet
	$(XCB) -destination 'generic/platform=iOS' -allowProvisioningUpdates build

open: Ply.xcodeproj
	open Ply.xcodeproj

clean:
	rm -rf build Ply.xcodeproj
