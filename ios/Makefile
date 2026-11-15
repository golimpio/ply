# The ply iOS app. Everything builds from here, with Xcode 26+ and XcodeGen
# (brew install xcodegen):
#
#   make            the project, then a simulator build
#   make test       the core unit tests on this Mac (no simulator needed)
#   make app-test   the tests hosted in the iPhone/iPad app, on the simulator
#   make tv-test    the same tests hosted in the Apple TV app, on the TV simulator
#   make check      all three
#   make run        build, boot the simulator, install and launch
#   make tv         the Apple TV app, built for the tvOS simulator
#   make tv-run     build, boot the TV simulator, install and launch
#   make device TEAM=ABCDE12345   a build for a real device, signed by your team
#   make open       the project in Xcode
#   make clean
#
# SIM and TVSIM pick the simulators: a device name, exactly as `xcrun simctl list devices` shows it
# (the newest runtime that has it), or a UDID, as in make run SIM="iPhone 17 Pro". Left to their
# defaults, a Mac without those devices (an older Xcode) gets its newest iPhone and Apple TV instead;
# a name given on the command line must exist. TEAM is your Apple developer team id; `make device`
# saves it in .team (ignored by git), so a later regeneration keeps signing as well.
#
# Xcode 26 (Swift 6.2) is required: project.yml sets SWIFT_DEFAULT_ACTOR_ISOLATION and the sources
# use `nonisolated` on type declarations, neither of which an older toolchain knows.

SIM     ?= iPhone 18 Pro
TVSIM   ?= Apple TV 4K (4th generation)
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
# The UDID of the simulator named $(1), or with that UDID; failing that, of the newest one whose name
# starts with $(2), when $(2) is given. A line reads "    <name> (<UDID>) (<state>)"; the name is
# matched whole ("Apple TV 4K (4th generation)" is not its "(at 1080p)" twin), and the last match
# wins: runtimes are listed oldest first.
quote    = '$(subst ','\'',$(1))'
simid    = $(shell xcrun simctl list devices available | awk -v want=$(call quote,$(1)) -v prefix=$(call quote,$(2)) '{ line = $$0; sub(/^ +/, "", line); if (match(line, / \([0-9A-F-]+\) \([^()]*\) *$$/)) { name = substr(line, 1, RSTART - 1); id = substr(line, RSTART + 2, 36); if (name == want || id == want) found = id; else if (prefix != "" && index(name, prefix) == 1) newest = id } } END { print (found != "" ? found : newest) }')
# The fallback only for a default: `?=` gives SIM the origin "file", a command line "command line".
fallback = $(if $(filter file,$(origin $(1))),$(2))
SIMID   := $(call simid,$(SIM),$(call fallback,SIM,iPhone))
SIMDEST := -destination 'id=$(SIMID)'
TVSIMID := $(call simid,$(TVSIM),$(call fallback,TVSIM,Apple TV))
TVSIMDEST := -destination 'id=$(TVSIMID)'
# Stops with a hint when no simulator matched: $(1) the UDID found, $(2) the name asked, $(3) its variable.
need-simulator = @test -n "$(1)" || { echo "no simulator named $(2): pick one of xcrun simctl list devices available, as $(3)=\"<name>\""; exit 1; }

.PHONY: all project build test app-test tv-test check ci-build run tv tv-run device open clean simulator tvsimulator toolchain

all: build

# Regenerated whenever the spec or any source file (added, removed, changed) is newer. A name with
# a space (an asset catalog's "App Icon - App Store.imagestack") is escaped here, since make would
# split it; no other shell character in a file name is escaped.
Ply.xcodeproj: project.yml $(shell find Ply PlyTV PlyCoreTests PlyAppTests -print | sed 's/ /\\ /g')
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
	$(call need-simulator,$(SIMID),$(SIM),SIM)

tvsimulator:
	$(call need-simulator,$(TVSIMID),$(TVSIM),TVSIM)

build: Ply.xcodeproj simulator toolchain
	$(XCB) $(SIMDEST) build

# CI has no simulator to boot: a generic destination compiles the app for the simulator platform.
ci-build: Ply.xcodeproj toolchain
	$(XCB) -destination 'generic/platform=iOS Simulator' build

# The core tests are a macOS unit-test bundle: no simulator, no test host, no debugger. The arch is
# named because the Mac is listed once per architecture it can run.
test: Ply.xcodeproj toolchain
	$(XCB_CORE) -destination 'platform=macOS,arch=$(shell uname -m)' test

# The app-hosted tests (PlyAppTests). They need a released Xcode (27.2 works): the 27.0 beta never
# resumed the test host it starts suspended, so a run hung until it timed out.
app-test: Ply.xcodeproj simulator toolchain
	$(XCB) $(SIMDEST) test

tv-test: Ply.xcodeproj tvsimulator toolchain
	$(XCB_TV) $(TVSIMDEST) test

check: test app-test tv-test

run: build
	xcrun simctl boot $(SIMID) 2>/dev/null || true
	open -a Simulator 2>/dev/null || true
	xcrun simctl install $(SIMID) "$(APP)"
	xcrun simctl launch $(SIMID) $(BUNDLE)

tv: Ply.xcodeproj toolchain
	$(XCB_TV) -destination 'generic/platform=tvOS Simulator' build

tv-run: tv tvsimulator
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
