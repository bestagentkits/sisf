# Smoke: managed shop released and rolled back on real Docker

Kết quả: **PASS** ở lần chạy thứ 10 (2026-11-16 07:34Z, commit `e821e97`). Cửa hàng WooCommerce đi qua đúng đường release của factory: Docker filter vai trò deploy, Compose backend, edge `Splitter` và `release.Pipeline`. Một bản cập nhật tốt được release trọn vẹn. Một bản cập nhật hỏng bị phát hiện qua tỉ lệ 5xx và rollback tự động. Dữ liệu đơn hàng có từ trước giữ nguyên, và backup khôi phục ra bản sao khớp hoàn toàn.

Chạy lại bằng: `SISF=<sisf binary> examples/woocommerce-app/bin/release-smoke.sh [--keep]`. Mỗi lần chạy ghi một dòng JSON vào `~/.sisf-trial/release-smoke.jsonl`.

## Bằng chứng

| Hạng mục | Kết quả |
|---|---|
| Bản tốt (WooCommerce 11.2.1 → 11.2.2) | `closed` sau 4 khung 5/25/100%; revision quan sát được là `pair(v2)` = `sha256:2a5245…c400` |
| Mua hàng qua edge sau bản tốt | Qua (`checkrun` với check `purchase`, thanh toán sandbox) |
| Bản hỏng (plugin shop gọi hàm không tồn tại) | Khung 5%: 20 mẫu, 20 lỗi 5xx → `continue` (khung vi phạm 1/2). Khung 25%: 100 mẫu, 100 lỗi → `rollback`. Cause `BlockRolledBack`, khôi phục về `pair(v2)` |
| Edge sau rollback | `/healthz` trả 200, header revision là `pair(v2)` |
| Traffic | 3200 request: 3072 trả 200, 120 trả 500 (toàn bộ đến từ canary hỏng), 8 timeout phía client |
| Dữ liệu | products 100 → 100, customers 20 → 20, orders 50 → 51 (thêm đúng lần mua hàng); sha256 của 50 đơn hàng gốc không đổi |
| Backup/restore | `mariadb-dump` sha `fc704fd8…0020`, khôi phục vào DB sao chép; sha của tập đơn hàng trên bản sao khớp với DB live |
| Thời gian | Test smoke 1167 s, toàn bộ script 2217 s |

## Lỗi gặp phải và cách sửa (lần 1–9)

1. `CheckPinned` từ chối artifact dạng cặp. Đã sửa: artifact có bundle phải có `PairDigest(image, bundle) == Digest`.
2. Revision không bao giờ báo healthy. Nguyên nhân: JSON health của plugin thiếu `"status":"ok"`. Đã sửa plugin.
3. Pipeline cần một observer của các byte → thêm `appbundle.PairObserver`.
4. Issue chưa có record → seed issue lên ReleaseCandidate.
5. Không đủ traffic thật → trước mỗi release, driver gửi một khung request thật.
6. Ngày quan sát lệch đồng hồ → dùng chung một live clock. Edge ghi Host bằng tên container, khiến WordPress redirect sang host đó → ghim host trong `wp-config.php`.
7. Lease hết hạn giữa chừng (TTL mặc định bằng 3 lần khung tua) → đặt `LeaseTTL: 30m` cho smoke.
8. Khung 5% chỉ đếm được 13/20 mẫu vì traffic mang ngày sau cuối khung → gửi traffic trước, rồi mới tua phần còn lại của khung.
9. Bản hỏng bị gỡ vì lý do "unjudged" (traffic thấp) chứ không phải vì phát hiện lỗi. Đây không phải bằng chứng hợp lệ. Đã đổi khung thành 5 phút để mọi request chậm vẫn mang ngày bên trong khung, và test giờ bắt buộc cause là `BlockRolledBack`.

## Nhận xét cho sản phẩm

- Một canary trả lỗi 100% vẫn được nâng từ 5% lên 25% trước khi bị rollback, vì `Consecutive` mặc định là 2 bất kể tỉ lệ lỗi cao đến đâu. Trong smoke này 120 request của khách nhận lỗi 500. Một ngưỡng "rollback ngay ở khung đầu khi tỉ lệ lỗi vượt xa ngưỡng" sẽ cắt con số đó xuống còn 20. Việc này thuộc chính sách release dùng chung, nên được tách thành issue riêng thay vì sửa trong PR này.
- Một cửa hàng ít traffic cần có synthetic suite. Nếu không có, pipeline chỉ có thể hold hoặc trả về unjudged. Phần wiring supervisor phải lấy các check read-only trong Spec làm synthetic suite.

## Câu hỏi còn mở

- Không có.
