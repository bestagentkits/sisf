<?php
/**
 * Seeds the shop with simulated data, deterministically: 100 products with
 * stock, 20 customers, 50 paid orders, coupons, shipping methods, and the
 * sandbox and offline payment methods. All names and addresses are made up.
 * It refuses to run twice: a seeded shop keeps its data.
 *
 * Run: wp eval-file seed/seed.php
 */
if ( get_option( 'sisf_seeded' ) ) {
	WP_CLI::log( 'already seeded' );
	return;
}
mt_srand( 20261113 );
// Seeding sends no mail: the addresses are made up.
add_filter( 'pre_wp_mail', '__return_true' );

update_option( 'woocommerce_currency', 'USD' );
update_option( 'woocommerce_default_country', 'US:CA' );
update_option( 'woocommerce_manage_stock', 'yes' );
update_option( 'woocommerce_calc_taxes', 'no' );
update_option( 'woocommerce_enable_coupons', 'yes' );
update_option( 'woocommerce_bacs_settings', array( 'enabled' => 'yes', 'title' => 'Bank transfer (offline)' ) );
update_option( 'woocommerce_cod_settings', array( 'enabled' => 'yes', 'title' => 'Cash on delivery (offline)' ) );

// Products.
$cats = array();
foreach ( array( 'Coffee', 'Tea', 'Mugs', 'Grinders', 'Gift sets' ) as $name ) {
	$term   = wp_insert_term( $name, 'product_cat' );
	$cats[] = is_wp_error( $term ) ? (int) $term->get_error_data() : (int) $term['term_id'];
}
$product_ids = array();
for ( $i = 1; $i <= 100; $i++ ) {
	$p = new WC_Product_Simple();
	$p->set_name( sprintf( 'Trial product %03d', $i ) );
	$p->set_sku( sprintf( 'TRIAL-%03d', $i ) );
	$p->set_regular_price( (string) ( 5 + ( $i * 37 ) % 95 ) . '.00' );
	$p->set_manage_stock( true );
	$p->set_stock_quantity( 0 === $i % 10 ? 3 : 50 + $i % 40 );
	$p->set_category_ids( array( $cats[ $i % count( $cats ) ] ) );
	$p->set_status( 'publish' );
	$product_ids[] = $p->save();
}

// Customers.
$first = array( 'An', 'Binh', 'Chi', 'Dung', 'Giang', 'Hoa', 'Khanh', 'Lan', 'Minh', 'Nam' );
$last  = array( 'Trial', 'Sample' );
$customer_ids = array();
for ( $i = 1; $i <= 20; $i++ ) {
	$c = new WC_Customer();
	$c->set_username( sprintf( 'customer%02d', $i ) );
	$c->set_email( sprintf( 'customer%02d@shop.example.invalid', $i ) );
	$c->set_password( wp_generate_password( 32 ) );
	$c->set_first_name( $first[ $i % 10 ] );
	$c->set_last_name( $last[ $i % 2 ] );
	$c->set_billing_first_name( $first[ $i % 10 ] );
	$c->set_billing_last_name( $last[ $i % 2 ] );
	$c->set_billing_address_1( sprintf( '%d Example Street', $i ) );
	$c->set_billing_city( 'Sampletown' );
	$c->set_billing_postcode( '94000' );
	$c->set_billing_country( 'US' );
	$c->set_billing_state( 'CA' );
	$c->set_billing_email( $c->get_email() );
	$customer_ids[] = $c->save();
}

// Coupons.
foreach ( array( array( 'TRIAL10', 'percent', '10' ), array( 'SHIPFREE', 'fixed_cart', '0' ), array( 'WELCOME5', 'fixed_cart', '5' ) ) as $def ) {
	$coupon = new WC_Coupon();
	$coupon->set_code( $def[0] );
	$coupon->set_discount_type( $def[1] );
	$coupon->set_amount( $def[2] );
	$coupon->set_free_shipping( 'SHIPFREE' === $def[0] );
	$coupon->save();
}

// Shipping: one zone for the US with flat rate and free shipping.
$zone = new WC_Shipping_Zone();
$zone->set_zone_name( 'United States' );
$zone->add_location( 'US', 'country' );
$zone->save();
$flat = $zone->add_shipping_method( 'flat_rate' );
update_option( 'woocommerce_flat_rate_' . $flat . '_settings', array( 'title' => 'Flat rate', 'cost' => '7', 'tax_status' => 'none' ) );
$free = $zone->add_shipping_method( 'free_shipping' );
update_option( 'woocommerce_free_shipping_' . $free . '_settings', array( 'title' => 'Free shipping', 'requires' => 'coupon' ) );

// Orders: 50 paid orders through the sandbox gateway.
for ( $i = 1; $i <= 50; $i++ ) {
	$order = wc_create_order( array( 'customer_id' => $customer_ids[ $i % 20 ] ) );
	$lines = 1 + $i % 3;
	for ( $l = 0; $l < $lines; $l++ ) {
		$order->add_product( wc_get_product( $product_ids[ ( $i * 7 + $l * 13 ) % 100 ] ), 1 + $l );
	}
	$customer = new WC_Customer( $customer_ids[ $i % 20 ] );
	$order->set_address( $customer->get_billing(), 'billing' );
	$order->set_address( $customer->get_billing(), 'shipping' );
	$rate = new WC_Shipping_Rate( 'flat_rate:' . $flat, 'Flat rate', 7, array(), 'flat_rate' );
	$item = new WC_Order_Item_Shipping();
	$item->set_shipping_rate( $rate );
	$order->add_item( $item );
	$order->set_payment_method( 'sisf_sandbox' );
	$order->set_payment_method_title( 'Sandbox payment (no money moves)' );
	$order->calculate_totals();
	$order->payment_complete( sprintf( 'sandbox-seed-%03d', $i ) );
	if ( 0 === $i % 2 ) {
		$order->update_status( 'completed' );
	}
	wc_reduce_stock_levels( $order->get_id() );
}

update_option( 'sisf_seeded', gmdate( 'c' ) );
WP_CLI::success( 'seeded 100 products, 20 customers, 3 coupons, 2 shipping methods, 50 orders' );
