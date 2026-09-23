#!/bin/sh
# Signe une app (dist/Jarvis.app) avec l'identité locale stable créée par
# scripts/signing_identity.sh. Sans elle : signature ad hoc, et un
# avertissement — macOS pourra redemander l'accès au dossier Documents
# après chaque recompilation.
set -eu

APP="${1:?usage: sign_app.sh chemin/vers/App.app}"
NAME="Jarvis Local Signing"
DIR="$HOME/.jarvis/signing"
KEYCHAIN="$DIR/jarvis.keychain-db"
PASSFILE="$DIR/keychain-password"
BUNDLE_ID=$(/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "$APP/Contents/Info.plist")

if [ ! -f "$KEYCHAIN" ] || [ ! -f "$PASSFILE" ]; then
	echo "ATTENTION : pas d'identité de signature locale (make signing-identity) — signature ad hoc." >&2
	codesign --force --sign - --identifier "$BUNDLE_ID" "$APP"
	exit 0
fi

security unlock-keychain -p "$(cat "$PASSFILE")" "$KEYCHAIN"
ensure_in_search_list() {
	# codesign ne cherche une identité que dans la liste de recherche des
	# trousseaux, même avec --keychain : le trousseau dédié y est ajouté
	# (à la suite des trousseaux existants).
	if ! security list-keychains -d user | grep -q "$KEYCHAIN"; then
		# shellcheck disable=SC2046
		security list-keychains -d user -s $(security list-keychains -d user | tr -d '"') "$KEYCHAIN"
	fi
}
ensure_in_search_list

# Signée dans un dossier temporaire, hors de ~/Documents : sur cette
# machine, iCloud Drive synchronise Documents et pose ses attributs
# étendus (FinderInfo, fileprovider) sur les fichiers pendant qu'on les
# signe — codesign refuse alors (« detritus not allowed »), par
# intermittence. La signature vit dans le bundle (_CodeSignature et
# binaire), pas dans les attributs : la recopie sans attributs la garde.
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
ditto --noextattr --norsrc "$APP" "$STAGE/app.app"
# Empreinte SHA-1 du certificat : codesign la préfère au nom (non ambigu),
# et un certificat auto-signé n'a pas à être « de confiance » pour signer.
HASH=$(security find-certificate -c "$NAME" -Z "$KEYCHAIN" | awk '/SHA-1 hash:/ {print $3}')
codesign --force --sign "$HASH" --keychain "$KEYCHAIN" --identifier "$BUNDLE_ID" --timestamp=none "$STAGE/app.app"
rm -rf "$APP"
ditto --noextattr --norsrc "$STAGE/app.app" "$APP"
# Pas de --strict : iCloud repose ses attributs sur le bundle dans les
# secondes qui suivent, ce que seul le mode strict refuse. La signature
# reste valide et satisfait son exigence (ce que vérifient le lancement
# et les autorisations de macOS).
codesign --verify "$APP"
echo "$APP signé ($NAME, $BUNDLE_ID)"
