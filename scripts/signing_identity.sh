#!/bin/sh
# Crée (une fois) l'identité de signature locale de Jarvis.app : un
# certificat auto-signé de signature de code, dans un trousseau dédié
# (~/.jarvis/signing/jarvis.keychain-db).
#
# Pourquoi : signé ad hoc, Jarvis.app est reconnu par macOS à l'empreinte
# de son binaire — une recompilation en fait une « nouvelle » application,
# et l'autorisation d'accès au dossier Documents (où vit le dépôt) peut
# être perdue (vu en réel : ticket "Déplacer le filtre date documents").
# Signé avec ce certificat, l'app est reconnue par son identifiant et le
# certificat, stables d'une compilation à l'autre.
#
# Trousseau dédié, mot de passe aléatoire gardé à côté (0600) : signer ne
# demande jamais le mot de passe de session, et rien ne touche au
# trousseau de session. Le certificat ne sert qu'à signer Jarvis sur
# cette machine ; il n'est reconnu par personne d'autre.
set -eu

NAME="Jarvis Local Signing"
DIR="$HOME/.jarvis/signing"
KEYCHAIN="$DIR/jarvis.keychain-db"
PASSFILE="$DIR/keychain-password"

if [ -f "$KEYCHAIN" ] && security find-certificate -c "$NAME" "$KEYCHAIN" >/dev/null 2>&1; then
	echo "Identité « $NAME » déjà présente dans $KEYCHAIN"
	exit 0
fi

mkdir -p "$DIR"
chmod 700 "$DIR"
umask 077
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

cat >"$TMP/cert.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, codeSigning
subjectKeyIdentifier = hash
EOF

openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
	-config "$TMP/cert.cnf" -keyout "$TMP/key.pem" -out "$TMP/cert.pem" 2>/dev/null
P12PASS=$(openssl rand -hex 16)
openssl pkcs12 -export -inkey "$TMP/key.pem" -in "$TMP/cert.pem" \
	-name "$NAME" -passout "pass:$P12PASS" -out "$TMP/identity.p12"

[ -f "$PASSFILE" ] || openssl rand -hex 24 >"$PASSFILE"
chmod 600 "$PASSFILE"
KCPASS=$(cat "$PASSFILE")
[ -f "$KEYCHAIN" ] || security create-keychain -p "$KCPASS" "$KEYCHAIN"
security set-keychain-settings "$KEYCHAIN" # pas de verrouillage automatique
security unlock-keychain -p "$KCPASS" "$KEYCHAIN"
security import "$TMP/identity.p12" -k "$KEYCHAIN" -P "$P12PASS" -T /usr/bin/codesign >/dev/null
# Sans cette liste de partition, codesign demanderait l'autorisation
# d'utiliser la clé à chaque signature.
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$KCPASS" "$KEYCHAIN" >/dev/null

# codesign ne cherche une identité que dans la liste de recherche des
# trousseaux : le trousseau dédié y est ajouté, à la suite des existants.
if ! security list-keychains -d user | grep -q "$KEYCHAIN"; then
	# shellcheck disable=SC2046
	security list-keychains -d user -s $(security list-keychains -d user | tr -d '"') "$KEYCHAIN"
fi

echo "Identité « $NAME » créée dans $KEYCHAIN"
