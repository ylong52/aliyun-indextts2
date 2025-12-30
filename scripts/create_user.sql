-- ============================================
-- 创建数据库用户脚本（可选，推荐用于生产环境）
-- ============================================

-- 注意：请修改 'your_secure_password' 为强密码

-- 创建用户（本地访问）
CREATE USER IF NOT EXISTS 'vibevoice_user'@'localhost' IDENTIFIED BY 'your_secure_password';

-- 创建用户（远程访问，可选）
CREATE USER IF NOT EXISTS 'vibevoice_user'@'%' IDENTIFIED BY 'your_secure_password';

-- 授予权限
GRANT ALL PRIVILEGES ON `vibevoice`.* TO 'vibevoice_user'@'localhost';
GRANT ALL PRIVILEGES ON `vibevoice`.* TO 'vibevoice_user'@'%';

-- 刷新权限
FLUSH PRIVILEGES;

-- 查看用户权限
SHOW GRANTS FOR 'vibevoice_user'@'localhost';
SHOW GRANTS FOR 'vibevoice_user'@'%';

SELECT 'Database user created successfully!' AS message;
SELECT 'Please remember to change the password!' AS warning;

