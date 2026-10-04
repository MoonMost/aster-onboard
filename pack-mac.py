#!/usr/bin/env python3
"""把 darwin 二进制组装成 macOS 的 .app，并压成 zip。

关键点：zip 里要显式写入 Unix 权限位（0755）。Windows 打包默认不带这些位，
解压到 Mac 上二进制就没有执行权限，双击会报"应用程序已损坏"。
"""
import os
import shutil
import zipfile

ROOT = os.path.dirname(os.path.abspath(__file__))
BUNDLE_ID = "top.xiaoshi888.aster-onboard"
ICNS = os.path.join(ROOT, "AsterOnboard.icns")

INFO_PLIST = """<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>AsterOnboard</string>
  <key>CFBundleDisplayName</key><string>Aster 接入导入器</string>
  <key>CFBundleIdentifier</key><string>{bundle_id}</string>
  <key>CFBundleExecutable</key><string>AsterOnboard</string>
  <key>CFBundleIconFile</key><string>AsterOnboard</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleSignature</key><string>????</string>
  <key>CFBundleVersion</key><string>1.0.0</string>
  <key>CFBundleShortVersionString</key><string>1.0.0</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
</dict>
</plist>
""".format(bundle_id=BUNDLE_ID)


def make_app(binary, out_dir):
    if not os.path.isfile(binary):
        raise SystemExit("找不到二进制：" + binary)
    app = os.path.join(out_dir, "AsterOnboard.app")
    if os.path.isdir(app):
        shutil.rmtree(app)
    macos_dir = os.path.join(app, "Contents", "MacOS")
    res_dir = os.path.join(app, "Contents", "Resources")
    os.makedirs(macos_dir)
    os.makedirs(res_dir)
    shutil.copy2(binary, os.path.join(macos_dir, "AsterOnboard"))
    with open(os.path.join(app, "Contents", "Info.plist"), "w", encoding="utf-8") as f:
        f.write(INFO_PLIST)
    with open(os.path.join(app, "Contents", "PkgInfo"), "w") as f:
        f.write("APPL????")
    if os.path.isfile(ICNS):
        shutil.copy2(ICNS, os.path.join(res_dir, "AsterOnboard.icns"))
    return app


def zip_app(app_dir, zip_path):
    parent = os.path.dirname(app_dir)
    app_name = os.path.basename(app_dir)
    if os.path.exists(zip_path):
        os.remove(zip_path)
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as z:
        for base, dirs, files in os.walk(app_dir):
            rel_dir = os.path.relpath(base, parent).replace(os.sep, "/")
            zi = zipfile.ZipInfo(rel_dir + "/")
            zi.create_system = 3          # Unix
            zi.external_attr = (0o40755 << 16) | 0x10
            z.writestr(zi, b"")
            for name in files:
                full = os.path.join(base, name)
                rel = os.path.relpath(full, parent).replace(os.sep, "/")
                exec_bit = name == "AsterOnboard" and os.sep + "MacOS" + os.sep in full
                zi = zipfile.ZipInfo(rel)
                zi.create_system = 3      # Unix（Archive Utility 才会按权限位解压）
                zi.external_attr = (0o100755 if exec_bit else 0o100644) << 16
                zi.compress_type = zipfile.ZIP_DEFLATED
                with open(full, "rb") as f:
                    z.writestr(zi, f.read())
    return zip_path


def main():
    targets = [
        ("dist/mac-arm64/AsterOnboard", "dist/AsterOnboard-mac-arm64.zip"),
        ("dist/mac-intel/AsterOnboard", "dist/AsterOnboard-mac-intel.zip"),
    ]
    for binary, zip_path in targets:
        binary = os.path.join(ROOT, binary)
        zip_path = os.path.join(ROOT, zip_path)
        out_dir = os.path.join(ROOT, os.path.dirname(zip_path))
        os.makedirs(out_dir, exist_ok=True)
        app = make_app(binary, os.path.join(out_dir, os.path.basename(binary)))
        zip_app(app, zip_path)
        print("打包完成:", os.path.relpath(zip_path, ROOT),
              "(%.1f MB)" % (os.path.getsize(zip_path) / 1024 / 1024))


if __name__ == "__main__":
    main()
