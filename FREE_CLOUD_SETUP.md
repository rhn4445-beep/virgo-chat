# Virgo: 100% Free 24/7 Cloud Server & CI/CD Pipeline

This guide outlines how to host your Virgo chat server in the cloud 24/7 for **$0**, and how to build Android APKs and Windows apps automatically using GitHub Actions without relying on your local PC.

---

## 1. Push Code to GitHub

1. Create a new repository on [GitHub](https://github.com/new) (e.g. `virgo-chat`).
2. Open PowerShell in `C:\chat-gateway` and run:
   ```powershell
   git init
   git add .
   git commit -m "Initial commit with Dockerfile and GitHub Actions"
   git branch -M main
   git remote add origin https://github.com/<YOUR_USERNAME>/<YOUR_REPO>.git
   git push -u origin main
   ```

---

## 2. Deploy Server 24/7 on Render.com (100% Free)

1. Sign up for a free account at [Render.com](https://render.com).
2. Click **New +** -> **Web Service**.
3. Select **Build and deploy from a Git repository** and connect your GitHub repo.
4. Render will detect the `Dockerfile` automatically:
   - **Name**: `virgo-chat` (or any name you prefer)
   - **Region**: Choose the closest region to you (e.g., Oregon, Frankfurt, Singapore)
   - **Branch**: `main`
   - **Instance Type**: **Free**
5. Click **Create Web Service**.
6. Once deployed (typically 1–2 minutes), Render gives you a permanent HTTPS URL:
   `https://virgo-chat.onrender.com`

---

## 3. Keep Server Awake 24/7 (Prevent Free Sleep)

Render's free tier spins down after 15 minutes of inactivity. To prevent this and keep it active 24/7/365:

1. Create a free account at [cron-job.org](https://cron-job.org) or [UptimeRobot](https://uptimerobot.com).
2. Add a new monitor / cron job:
   - **URL**: `https://virgo-chat.onrender.com/`
   - **Interval**: Every 10 minutes (or 14 minutes).
3. That's it! Your server will now stay awake 24/7 with zero spin-down delay.

---

## 4. Automatic Cloud Builds with GitHub Actions

Every time you push code to `main` (or click **Run workflow** under the **Actions** tab on GitHub):

GitHub Actions automatically starts 3 parallel virtual machines in the cloud:
1. **Android Worker (Ubuntu)**: Compiles `app-debug.apk` with Gradle.
2. **Windows Desktop Worker (Windows Server)**: Compiles `gateway.exe` and packages `virgo-windows-desktop.zip` via PyInstaller.
3. **Linux Server Worker (Ubuntu)**: Compiles the static `gateway-linux` binary.

### How to Download Builds:
1. Go to your GitHub repository -> click **Actions**.
2. Click on the latest workflow run.
3. Scroll down to **Artifacts** to download:
   - `virgo-android-debug-apk`
   - `virgo-windows-desktop`
   - `virgo-linux-server-binary`

---

## 5. Connecting Android & Desktop to Your Cloud Server

Once your Render URL is live (e.g. `https://virgo-chat.onrender.com`):

1. **For Android**: In `capacitor.config.json`, change:
   ```json
   "server": {
     "url": "https://virgo-chat.onrender.com",
     "cleartext": true
   }
   ```
2. **For Web**: Simply open `https://virgo-chat.onrender.com` from any phone or computer browser!
