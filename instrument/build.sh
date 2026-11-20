#!/usr/bin/env bash
# Build the droidjev instrumentation APK with the raw SDK tools — no Gradle,
# no wrapper, no third-party deps: javac + d8 + aapt2 + zipalign + apksigner.
#
# Local dev: generates a gitignored debug keystore on first run
# (instrument/debug.keystore). CI signs with the release keystore from
# DROIDJEV_KEYSTORE_B64 / DROIDJEV_KEYSTORE_PASSWORD / DROIDJEV_KEY_ALIAS.
set -euo pipefail
cd "$(dirname "$0")"

SDK="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
BT="$SDK/build-tools/${DROIDJEV_BUILD_TOOLS:-$(ls "$SDK/build-tools" | sort -V | tail -1)}"
PLATFORM="${DROIDJEV_PLATFORM:-android-$(ls "$SDK/platforms" | sed 's/android-//' | sort -V | tail -1)}"
ANDROID_JAR="$SDK/platforms/$PLATFORM/android.jar"
for tool in javac d8 aapt2 zipalign apksigner; do
  command -v "$tool" >/dev/null 2>&1 || command -v "$BT/$tool" >/dev/null 2>&1 || {
    echo "missing build tool: $tool (build-tools $BT, SDK: $SDK)" >&2; exit 1; }
done

rm -rf build && mkdir -p build/classes build/out
echo "== javac ($PLATFORM) =="
javac -source 8 -target 8 -nowarn -Xlint:-options \
  -classpath "$ANDROID_JAR" -d build/classes \
  java/dev/droidjev/instrument/*.java

echo "== d8 =="
"$BT/d8" --release --min-api 24 --lib "$ANDROID_JAR" \
  --output build/out build/classes/dev/droidjev/instrument/*.class

echo "== aapt2 =="
"$BT/aapt2" link -I "$ANDROID_JAR" \
  --manifest AndroidManifest.xml \
  --min-sdk-version 24 --target-sdk-version 30 \
  -o build/unsigned.apk

echo "== package =="
(cd build/out && zip -q ../unsigned.apk classes.dex)
"$BT/zipalign" -f 4 build/unsigned.apk build/aligned.apk

echo "== sign =="
if [ -n "${DROIDJEV_KEYSTORE_B64:-}" ]; then
  echo "$DROIDJEV_KEYSTORE_B64" | base64 -d > build/release.keystore
  KS=build/release.keystore
  KS_PASS="${DROIDJEV_KEYSTORE_PASSWORD:?DROIDJEV_KEYSTORE_PASSWORD required}"
  KS_ALIAS="${DROIDJEV_KEY_ALIAS:?DROIDJEV_KEY_ALIAS required}"
else
  KS=debug.keystore
  if [ ! -f "$KS" ]; then
    keytool -genkeypair -keystore "$KS" -alias droidjev -keyalg RSA -keysize 2048 \
      -validity 10950 -storepass droidjev -keypass droidjev \
      -dname "CN=droidjev debug, O=droidjev"
  fi
  KS_PASS=droidjev
  KS_ALIAS=droidjev
fi
"$BT/apksigner" sign --ks "$KS" --ks-pass "pass:$KS_PASS" --ks-key-alias "$KS_ALIAS" \
  --out droidjev-instrument.apk build/aligned.apk

"$BT/apksigner" verify droidjev-instrument.apk
ls -l droidjev-instrument.apk
