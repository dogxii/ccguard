#!/bin/sh
set -eu

CCG_SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CCG_DEST_DIR=${CCG_INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$CCG_DEST_DIR"

if [ -e "$CCG_DEST_DIR/ccg" ]; then
  case "$(go version -m "$CCG_DEST_DIR/ccg" 2>/dev/null || true)" in
    *ccguard/cmd/ccg*) ;;
    *) echo "已有其他 ccg 命令，安装已停止。" >&2; exit 1 ;;
  esac
fi
if [ -e "$CCG_DEST_DIR/ccg-browser" ] || [ -L "$CCG_DEST_DIR/ccg-browser" ]; then
  case "$(readlink "$CCG_DEST_DIR/ccg-browser" || true)" in
    ccg|"$CCG_DEST_DIR/ccg") ;;
    *) echo "已有其他 ccg-browser 文件，安装已停止。" >&2; exit 1 ;;
  esac
fi

CCG_BUILD_FILE=$(mktemp "$CCG_DEST_DIR/.ccg-build.XXXXXX")
trap 'rm -f "$CCG_BUILD_FILE"' EXIT HUP INT TERM
cd "$CCG_SOURCE_DIR"
go build -trimpath -ldflags='-s -w' -o "$CCG_BUILD_FILE" ./cmd/ccg
chmod 755 "$CCG_BUILD_FILE"
mv -f "$CCG_BUILD_FILE" "$CCG_DEST_DIR/ccg"
ln -sf ccg "$CCG_DEST_DIR/ccg-browser"
"$CCG_DEST_DIR/ccg" init
echo "已安装：$CCG_DEST_DIR/ccg"
echo "若连接服务正在运行，请在会话结束后运行 ccg service restart 以使用新版本。"
case ":$PATH:" in
  *":$CCG_DEST_DIR:"*) ;;
  *) echo "安装目录不在 PATH；请将其加入 shell PATH。安装脚本不会改写 shell 配置。" ;;
esac
