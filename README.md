# 🛡️ db-saver

**db-saver** est un utilitaire léger, moderne et sécurisé conçu pour automatiser la sauvegarde de vos bases de données Docker (**PostgreSQL** et **SQLite**) et les synchroniser vers n'importe quel stockage cloud via **rclone** (S3, Google Drive, Backblaze B2, SFTP, etc.).

Idéal pour les déploiements gérés avec **Dokploy** ou tout hôte Docker autonome.

---

## ✨ Fonctionnalités

- 🐳 **Découverte automatique des conteneurs Docker** : Détecte les conteneurs actifs, identifie les bases de données (Postgres / SQLite) et pré-remplit les formulaires.
- 🐘 **Sauvegarde PostgreSQL propre** : Extraction via `docker exec pg_dump`, détection automatique des identifiants (`POSTGRES_USER`, `POSTGRES_DB`, `POSTGRES_PASSWORD`) avec surcharges manuelles possibles.
- 🗄️ **Sauvegarde SQLite sécurisée** : Extraction sans corruption via `sqlite3 VACUUM INTO` ou fallback propre par copie Docker archive (`docker cp`).
- 🗜️ **Compression gzip automatique** : Les fichiers SQL et SQLite sont compressés à la volée avant transfert.
- ☁️ **Synchronisation rclone** : Détection automatique des remotes configurés, éditeur web de `rclone.conf` dans l'interface, upload et listing distant.
- ⏱️ **Planification flexible** : Presets simples (Toutes les heures, toutes les 6h, tous les jours à 03h00, hebdomadaire) ou expressions Cron standard libres.
- 🧹 **Politique de rétention** : Conservation des *N* plus récentes sauvegardes ou suppression des sauvegardes vieilles de plus de *X* jours.
- 🔴 **Console de logs en direct (SSE)** : Bouton « Sauvegarder maintenant » avec suivi des logs pas à pas en streaming direct.
- 🔄 **Téléchargement & Restauration** : Liste des sauvegardes stockées sur le remote avec téléchargement en 1 clic et restauration directe dans le conteneur cible.
- 🔔 **Alertes Webhooks** : Notifications en cas de succès ou d'échec sur **Discord**, **Telegram** ou **Webhook JSON générique**.
- 🔒 **Interface Web sécurisée** : Protégée par identifiant et mot de passe, session par cookie sécurisé, mode sombre soigné avec glassmorphism (Go + HTMX).

---

## 🚀 Déploiement sur Dokploy

Créez une nouvelle application ou un service Docker Compose dans **Dokploy** avec la configuration suivante :

### `docker-compose.yml`

```yaml
services:
  db-saver:
    image: ghcr.io/zipleix/db-saver:latest # ou build: .
    container_name: db-saver
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      - PORT=8080
      - ADMIN_USER=admin
      - ADMIN_PASSWORD=votre_mot_de_passe_robuste
      - DATA_DIR=/data
      - RCLONE_CONFIG_PATH=/config/rclone.conf
      - TZ=Europe/Paris
    volumes:
      # Accès au socket Docker hôte pour exécuter pg_dump / copier les fichiers SQLite
      - /var/run/docker.sock:/var/run/docker.sock:ro
      # Persistance de la base locale de db-saver et des fichiers temporaires
      - ./data:/data
      # Fichier de configuration rclone (peut être monté ou édité via l'UI)
      - ./config/rclone.conf:/config/rclone.conf
```

---

## ⚙️ Variables d'Environnement

| Variable | Description | Défaut |
| :--- | :--- | :--- |
| `PORT` | Port d'écoute du serveur web | `8080` |
| `ADMIN_USER` | Nom d'utilisateur pour la connexion | `admin` |
| `ADMIN_PASSWORD` | Mot de passe de l'interface web | `admin123` |
| `DATA_DIR` | Répertoire pour la base SQLite interne et temporaires | `/data` |
| `RCLONE_CONFIG_PATH`| Chemin vers le fichier de configuration rclone | `/config/rclone.conf` |
| `TMP_BACKUP_DIR` | Répertoire temporaire de compression locale | `/data/tmp` |

---

## 🛠️ Configuration de rclone

Deux méthodes simples pour configurer vos remotes de stockage :
1. **Via l'interface Web** : Rendez-vous dans **Paramètres & rclone**, collez votre configuration `rclone.conf` et cliquez sur *Enregistrer*.
2. **Via montage direct** : Placez votre fichier `rclone.conf` (généré avec `rclone config`) dans le dossier `./config/rclone.conf` de votre hôte.

---

## 💻 Développement Local

```bash
# Télécharger les dépendances
go mod download

# Lancer les tests unitaires
go test -v ./...

# Compiler et lancer
go run main.go
```

Accédez à `http://localhost:8080` avec les identifiants par défaut (`admin` / `admin123`).
