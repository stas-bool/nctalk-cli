#!/usr/bin/env bash
# Один раз создаёт self-signed codesigning identity "nctalk-dev" в login keychain.
# Зачем: macOS Application Firewall и TCC (микрофон) привязываются к code-signature
# бинарника. Неподписанный/ad-hoc Go-бинарник меняет подпись при каждой пересборке
# → фаервол спрашивает «разрешить входящие» каждый раз заново. Стабильная identity
# даёт постоянный designated requirement → после ОДНОГО разрешения больше не спрашивает.
#
# Запуск:  bash scripts/setup-codesign.sh
# Удалить: security delete-identity -c "nctalk-dev" "$KEYCHAIN"
#
# Идемпотентен: если identity уже есть — выходит без изменений.
set -euo pipefail

NAME="nctalk-dev"
KEYCHAIN="${CODESIGN_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
# Пароль только для p12 «в транзите»; после импорта в keychain не нужен.
PASS="nctalk-dev-local"

echo "→ keychain: $KEYCHAIN"

# уже есть?
if security find-identity -v -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "$NAME"; then
  echo "✔ identity '$NAME' уже существует — ничего делать не нужно."
  security find-identity -v -p codesigning "$KEYCHAIN" | grep "$NAME" || true
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "→ генерирую self-signed cert (codeSigning EKU, RSA 2048, 10 лет)…"
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" \
  -days 3650 -subj "/CN=$NAME" \
  -addext "extendedKeyUsage=codeSigning" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "keyUsage=critical,digitalSignature"

# Совместимость pkcs12 со старым macOS security (иначе «MAC verification failed»):
# openssl 3.x по умолчанию шифрует AES, macOS ждёт legacy-алгоритмы.
if openssl pkcs12 -help 2>&1 | grep -q -- '-legacy'; then
  P12LEGACY=(-legacy)
else
  # LibreSSL/openssl без -legacy — явно старые cipher
  P12LEGACY=(-certpbe PBE-SHA1-RC2-40 -keypbe PBE-SHA1-3DES -macalg sha1)
fi

echo "→ упаковываю в p12 (legacy cipher для совместимости с macOS security)…"
openssl pkcs12 -export "${P12LEGACY[@]}" \
  -inkey "$WORK/key.pem" -in "$WORK/cert.pem" \
  -out "$WORK/$NAME.p12" -name "$NAME" \
  -passout pass:"$PASS"

echo "→ импорт в keychain (codesign получает доступ без prompt)…"
# -T /usr/bin/codesign  — codesign может использовать ключ без GUI-вопроса
# -A                   — разрешить любому приложению доступ к ключу (dev only, см. ниже)
security import "$WORK/$NAME.p12" -k "$KEYCHAIN" -P "$PASS" -T /usr/bin/codesign -A

# Сразу дадим codesign доступ без возможного диалога (на случай если -A не подхватился):
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$PASS" "$KEYCHAIN" >/dev/null 2>&1 || true

echo "→ результат:"
security find-identity -v -p codesigning "$KEYCHAIN" | grep "$NAME" || true

cat <<EOF

✔ Готово. Identity '$NAME' в keychain.

Что дальше:
  make setup-codesign  # уже сделано этим скриптом
  make build-signed    # собирает nctalk/nctalk-call/nctalk-talk и подписывает их

ПРИ первом запуске подписанного бинарника macOS может ОДИН раз спросить
фаервол/TCC — разреши. Дальше пересборки (make build-signed) подпись стабильна,
повторных вопросов не будет.

Если codesign при подписи спросит доступ к ключу — нажми «Always Allow» (один раз).
EOF
