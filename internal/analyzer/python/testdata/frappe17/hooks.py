app_name = "safe_app"
app_title = "Safe title"
doc_events = {
    "Sales Order": "safe_app.events.sales_order",
    "Delivery Note": "safe_app.events.delivery_note",
}
scheduler_events = {"daily": ["safe_app.tasks.daily"]}
dynamic_key = frappe.conf.get(CONFIG_KEY)
literal_key = frappe.get_conf().get("ERP_MODE")
