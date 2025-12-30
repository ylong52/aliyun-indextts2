-- ============================================
-- VibeVoice 数据库初始化脚本
-- MySQL 8.0+
-- 创建时间: 2024-11-30
-- ============================================

-- 1. 创建数据库
CREATE DATABASE IF NOT EXISTS `vibevoice` 
DEFAULT CHARACTER SET utf8mb4 
COLLATE utf8mb4_unicode_ci;

USE `vibevoice`;

-- 2. 创建用户表
CREATE TABLE IF NOT EXISTS `users` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '用户ID',
    `username` VARCHAR(20) NOT NULL COMMENT '用户名',
    `email` VARCHAR(100) NOT NULL COMMENT '邮箱',
    `password` VARCHAR(255) NOT NULL COMMENT '加密后的密码',
    `status` ENUM('active', 'inactive', 'banned') NOT NULL DEFAULT 'active' COMMENT '用户状态',
    `last_login_at` DATETIME NULL DEFAULT NULL COMMENT '最后登录时间',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_username` (`username`),
    UNIQUE KEY `uk_email` (`email`),
    KEY `idx_status` (`status`),
    KEY `idx_created_at` (`created_at`)
) ENGINE=InnoDB 
DEFAULT CHARSET=utf8mb4 
COLLATE=utf8mb4_unicode_ci 
COMMENT='用户表';

-- 3. 创建文件表
CREATE TABLE IF NOT EXISTS `files` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '文件ID',
    `user_id` BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
    `file_name` VARCHAR(255) NOT NULL COMMENT '原始文件名',
    `stored_name` VARCHAR(255) NOT NULL COMMENT '存储文件名（UUID）',
    `file_path` VARCHAR(500) NOT NULL COMMENT '文件存储路径（相对路径）',
    `file_type` ENUM('file', 'audio', 'recording') NOT NULL COMMENT '文件类型',
    `mime_type` VARCHAR(100) NOT NULL COMMENT 'MIME 类型',
    `file_size` BIGINT UNSIGNED NOT NULL COMMENT '文件大小（字节）',
    `upload_method` ENUM('upload', 'audio', 'recording') NOT NULL COMMENT '上传方式',
    `description` TEXT NULL DEFAULT NULL COMMENT '文件描述',
    `status` ENUM('uploading', 'completed', 'failed') NOT NULL DEFAULT 'completed' COMMENT '文件状态',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    KEY `idx_user_id` (`user_id`),
    KEY `idx_file_type` (`file_type`),
    KEY `idx_upload_method` (`upload_method`),
    KEY `idx_status` (`status`),
    KEY `idx_created_at` (`created_at`),
    KEY `idx_user_file_type` (`user_id`, `file_type`),
    CONSTRAINT `fk_files_user_id` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB 
DEFAULT CHARSET=utf8mb4 
COLLATE=utf8mb4_unicode_ci 
COMMENT='文件表';

-- 4. 创建音频文件扩展表（可选，如果需要存储音频元数据）
CREATE TABLE IF NOT EXISTS `audio_files` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'ID',
    `file_id` BIGINT UNSIGNED NOT NULL COMMENT '文件ID',
    `duration` DECIMAL(10, 2) NULL DEFAULT NULL COMMENT '时长（秒）',
    `sample_rate` INT UNSIGNED NULL DEFAULT NULL COMMENT '采样率（Hz）',
    `bitrate` INT UNSIGNED NULL DEFAULT NULL COMMENT '比特率（bps）',
    `channels` TINYINT UNSIGNED NULL DEFAULT NULL COMMENT '声道数（1-单声道, 2-立体声）',
    `format` VARCHAR(20) NULL DEFAULT NULL COMMENT '音频格式（mp3, wav, m4a等）',
    `metadata` JSON NULL DEFAULT NULL COMMENT '其他元数据（JSON格式）',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_file_id` (`file_id`),
    KEY `idx_format` (`format`),
    CONSTRAINT `fk_audio_files_file_id` FOREIGN KEY (`file_id`) REFERENCES `files` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB 
DEFAULT CHARSET=utf8mb4 
COLLATE=utf8mb4_unicode_ci 
COMMENT='音频文件扩展信息表';

-- 5. 验证创建结果
SHOW TABLES;

-- 6. 显示表结构（验证）
SELECT '=== Users Table ===' AS info;
DESCRIBE `users`;

SELECT '=== Files Table ===' AS info;
DESCRIBE `files`;

SELECT '=== Audio Files Table ===' AS info;
DESCRIBE `audio_files`;

-- 7. 显示索引（验证）
SELECT '=== Users Indexes ===' AS info;
SHOW INDEX FROM `users`;

SELECT '=== Files Indexes ===' AS info;
SHOW INDEX FROM `files`;

SELECT '=== Audio Files Indexes ===' AS info;
SHOW INDEX FROM `audio_files`;

SELECT 'Database initialization completed successfully!' AS message;

