module.exports = {
  apps: [{
    name: 'omp-im',
    script: './omp-im',
    args: '--config config.json --log-level info',
    cwd: __dirname,
    autorestart: true,
    max_restarts: 10,
    min_uptime: '10s',
    restart_delay: 3000,
    max_memory_restart: '500M',
    log_date_format: 'YYYY-MM-DD HH:mm:ss Z',
    env: {
      NODE_ENV: 'production',
    },
  }],
};
