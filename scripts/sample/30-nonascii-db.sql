-- A database whose name is not ASCII.
SET NAMES utf8mb4;

DROP DATABASE IF EXISTS `サンプル`;
CREATE DATABASE `サンプル` DEFAULT CHARACTER SET utf8mb4;
USE `サンプル`;

CREATE TABLE `商品` (
  `番号` INT NOT NULL,
  `名前` VARCHAR(40) NOT NULL,
  PRIMARY KEY (`番号`)
);
INSERT INTO `商品` (`番号`, `名前`) VALUES
  (1, 'りんご'),
  (2, 'みかん'),
  (3, '🍇');
