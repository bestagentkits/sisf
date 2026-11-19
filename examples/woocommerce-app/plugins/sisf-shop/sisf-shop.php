<?php
/**
 * Plugin Name: SISF shop
 * Description: The shop's own rules: a loyalty discount for returning
 * customers and a low-stock notice on product pages. This is the code the
 * factory's agents maintain.
 * Version: 1.0.0
 */

/** Loyalty discount, in percent, for a customer with at least this many paid orders. */
const SISF_SHOP_LOYALTY_PERCENT = 5;
const SISF_SHOP_LOYALTY_ORDERS  = 3;
/** A product with this many items or fewer in stock shows a low-stock notice. */
const SISF_SHOP_LOW_STOCK = 5;

/**
 * Returns the loyalty discount of the cart's customer, as an amount.
 */
function sisf_shop_loyalty_discount( WC_Cart $cart ) {
	$customer = $cart->get_customer();
	if ( ! $customer || ! $customer->get_id() ) {
		return 0.0;
	}
	if ( (int) $customer->get_order_count() < SISF_SHOP_LOYALTY_ORDERS ) {
		return 0.0;
	}
	return round( (float) $cart->get_subtotal() * SISF_SHOP_LOYALTY_PERCENT / 100, wc_get_price_decimals() );
}

add_action(
	'woocommerce_cart_calculate_fees',
	function ( WC_Cart $cart ) {
		$discount = sisf_shop_loyalty_discount( $cart );
		if ( $discount > 0 ) {
			$cart->add_fee( 'Loyalty discount', -$discount );
		}
	}
);

add_action(
	'woocommerce_single_product_summary',
	function () {
		global $product;
		if ( ! $product || ! $product->managing_stock() ) {
			return;
		}
		$stock = (int) $product->get_stock_quantity();
		if ( $stock > 0 && $stock <= SISF_SHOP_LOW_STOCK ) {
			printf( '<p class="sisf-low-stock">Only %d left in stock.</p>', $stock );
		}
	},
	25
);
